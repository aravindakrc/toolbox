package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aravindakrc/toolbox/internal/engine"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

type recipeInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Inputs      []recipeInput `json:"inputs"`
	Steps       []string      `json:"steps"`
}

type recipeInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
}

type recipesOutput struct {
	Recipes []recipeInfo `json:"recipes"`
}

type runRecipeInput struct {
	Recipe   string            `json:"recipe" jsonschema:"name of the recipe"`
	Inputs   map[string]string `json:"inputs,omitempty" jsonschema:"values for the recipe inputs, as supplied by the user"`
	Bindings map[string]string `json:"bindings,omitempty" jsonschema:"slot bindings the user chose instead of the proposed ones, as Kind/name.slot to Kind/name"`
	Approved bool              `json:"approved,omitempty" jsonschema:"true only after the user has seen the plan and approved its wiring"`
}

// RecipeResult is what run_recipe returns.
type RecipeResult struct {
	Status  string           `json:"status" jsonschema:"ok, failed, running, needs_input or needs_human"`
	Message string           `json:"message,omitempty"`
	Plan    *recipe.Plan     `json:"plan,omitempty"`
	Reports []*engine.Report `json:"reports,omitempty"`
	Needs   []prompt.Need    `json:"needs,omitempty"`
}

// recipes loads the recipes, or none when the server has no recipe source.
func (s *server) recipes() (*recipe.Registry, error) {
	if s.cfg.Recipes == nil {
		return recipe.LoadRegistry()
	}
	return s.cfg.Recipes()
}

func (s *server) listRecipes(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, recipesOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, err := s.recipes()
	if err != nil {
		return nil, recipesOutput{}, err
	}
	out := recipesOutput{Recipes: []recipeInfo{}}
	for _, name := range reg.Names() {
		r, _ := reg.Get(name)
		info := recipeInfo{Name: name, Description: r.Description}
		for _, in := range r.Inputs {
			info.Inputs = append(info.Inputs, recipeInput{Name: in.Name, Description: in.Description, Required: in.Required(), Default: in.Default})
		}
		for _, step := range r.Steps {
			info.Steps = append(info.Steps, step.Object)
		}
		out.Recipes = append(out.Recipes, info)
	}
	return nil, out, nil
}

// recipeJob is a recipe run that outlives the tool call that started it.
type recipeJob struct {
	done   chan struct{}
	result RecipeResult
	err    error
}

// defaultRecipeWait keeps a tool call below the time clients allow for one.
const defaultRecipeWait = 30 * time.Second

// runRecipe runs the recipe in the background and waits a short while for it.
// Downloads and clones can take minutes, longer than a client waits for a
// tool call, so a slow run answers "running" and the next call for the same
// recipe waits on it and returns the outcome.
func (s *server) runRecipe(ctx context.Context, _ *mcp.CallToolRequest, in runRecipeInput) (*mcp.CallToolResult, RecipeResult, error) {
	s.jobsMu.Lock()
	job := s.jobs[in.Recipe]
	if job == nil {
		job = &recipeJob{done: make(chan struct{})}
		if s.jobs == nil {
			s.jobs = map[string]*recipeJob{}
		}
		s.jobs[in.Recipe] = job
		go func() {
			job.result, job.err = s.execRecipe(context.WithoutCancel(ctx), in)
			close(job.done)
		}()
	}
	s.jobsMu.Unlock()

	wait := s.cfg.RecipeWait
	if wait <= 0 {
		wait = defaultRecipeWait
	}
	select {
	case <-job.done:
		s.jobsMu.Lock()
		delete(s.jobs, in.Recipe)
		s.jobsMu.Unlock()
		return nil, job.result, job.err
	case <-time.After(wait):
		return nil, RecipeResult{Status: StatusRunning, Message: "The recipe is still running. Call run_recipe again " +
			"with the same recipe to wait for it; do not run its steps by hand."}, nil
	case <-ctx.Done():
		return nil, RecipeResult{}, ctx.Err()
	}
}

func (s *server) execRecipe(ctx context.Context, in runRecipeInput) (RecipeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, out, err := s.runRecipeLocked(ctx, in)
	return out, err
}

func (s *server) runRecipeLocked(ctx context.Context, in runRecipeInput) (*mcp.CallToolResult, RecipeResult, error) {

	reg, err := s.recipes()
	if err != nil {
		return nil, RecipeResult{}, err
	}
	rec, ok := reg.Get(in.Recipe)
	if !ok {
		return nil, RecipeResult{}, fmt.Errorf("unknown recipe %q", in.Recipe)
	}
	eng, err := s.engine(false)
	if err != nil {
		return nil, RecipeResult{}, err
	}
	runner := &recipe.Runner{Engine: eng, Store: s.cfg.Store}
	result, err := runner.Run(ctx, recipe.Request{Recipe: rec, Inputs: in.Inputs, Rebind: in.Bindings, Approved: in.Approved})

	out := RecipeResult{Status: StatusOK}
	if result != nil {
		out.Plan = &result.Plan
		out.Reports = result.Reports
	}

	var approval *recipe.ApprovalError
	var needsInput *prompt.NeedsInputError
	var needsHuman *prompt.NeedsHumanError
	switch {
	case err == nil:
		return nil, out, nil
	case errors.As(err, &approval):
		out.Status = StatusNeedsInput
		out.Message = "Show the plan to the user as a markdown table with one row per step and the columns Object, Action (create or reuse), Values (every field in set) and Uses (every slot binding, slot to object). Every create step lists the slot bindings it will make. " +
			"Ask the user to approve them or to name different ones, then call again with approved set " +
			"(and bindings for any change). Never approve on the user's behalf."
	case errors.As(err, &needsInput):
		out.Status = StatusNeedsInput
		out.Message = err.Error() + ". Ask the user, then call apply_object for that object with the answers, " +
			"or call this tool again with the missing inputs."
		out.Needs = needsInput.Needs
	case errors.As(err, &needsHuman):
		out.Status = StatusNeedsHuman
		out.Message = needsHuman.Message
	case result != nil:
		out.Status = StatusFailed
		out.Message = err.Error()
	default:
		return nil, RecipeResult{}, err
	}
	return nil, out, nil
}

// promptPrefix namespaces the prompts so they read as tbx commands.
const promptPrefix = "tbx-"

// addRecipePrompts offers every recipe as a prompt, which clients show as a
// command, so a user can start one without writing instructions. The prompt
// only tells the agent to use run_recipe and keeps the approval rule.
func addRecipePrompts(srv *mcp.Server, reg *recipe.Registry) {
	for _, name := range reg.Names() {
		r, _ := reg.Get(name)
		prompt := &mcp.Prompt{Name: promptPrefix + r.Name, Description: strings.Join(strings.Fields(r.Description), " ")}
		for _, in := range r.Inputs {
			prompt.Arguments = append(prompt.Arguments, &mcp.PromptArgument{Name: in.Name, Description: in.Description, Required: in.Required()})
		}
		srv.AddPrompt(prompt, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: prompt.Description,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: recipeRequest(r, req.Params.Arguments)}}},
			}, nil
		})
	}
}

func recipeRequest(r *recipe.Recipe, args map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Use the toolbox recipe %q (run_recipe tool).\n", r.Name)
	given := slices.Sorted(maps.Keys(args))
	if len(given) > 0 {
		b.WriteString("Inputs from me:\n")
		for _, k := range given {
			if args[k] != "" {
				fmt.Fprintf(&b, "- %s: %s\n", k, args[k])
			}
		}
	}
	b.WriteString("Ask me for any input that is still missing; never guess one. " +
		"Call run_recipe without approved first and show me the plan as a markdown table with one row per step and the columns Object, Action (create or reuse), Values (every field in set) and Uses (every slot binding, slot to object). " +
		"Only after I approve the wiring, call it again with approved set. " +
		"Do not start the application or mirrord, and do not commit anything.")
	return b.String()
}
