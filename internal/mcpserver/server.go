// Package mcpserver exposes the engine to agents over the Model Context Protocol.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aravindakrc/toolbox/internal/engine"
	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

const version = "0.1.0"

// Result statuses returned by the tools.
const (
	StatusOK         = "ok"
	StatusFailed     = "failed"
	StatusNeedsInput = "needs_input"
	StatusNeedsHuman = "needs_human"
	StatusRunning    = "running"
)

// Config configures the server.
type Config struct {
	// Kinds loads the kind registry. It is called for every tool call so that
	// edited kinds are picked up without a restart.
	Kinds func() (*kind.Registry, error)
	Store *object.Store
	// Recipes loads the recipe registry, like Kinds.
	Recipes  func() (*recipe.Registry, error)
	Platform platform.Info
	// RecipeWait is how long run_recipe waits for a run before answering
	// "running"; zero uses the default.
	RecipeWait time.Duration
	// Log receives provider output. It must not be the protocol stream.
	Log io.Writer
	// ReadOnly leaves out the tools that change anything.
	ReadOnly bool
	// Elicit asks the client to collect missing input from the user, instead
	// of reporting it to the agent. Only enable it for clients that support it.
	Elicit bool
}

type server struct {
	cfg Config
	// mu serialises tool calls because they read and write the same files.
	mu sync.Mutex

	jobsMu sync.Mutex
	jobs   map[string]*recipeJob
}

// New returns an MCP server for the engine.
func New(cfg Config) *mcp.Server {
	s := &server{cfg: cfg}
	srv := mcp.NewServer(&mcp.Implementation{Name: "tbx", Version: version}, nil)

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_kinds",
		Description: "List the kinds of object that can be created, with their fields and slots.",
		Annotations: readOnly,
	}, s.listKinds)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_objects",
		Description: "List existing objects, optionally of one kind.",
		Annotations: readOnly,
	}, s.listObjects)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "show_object",
		Description: "Show an object's fields, where each value comes from, and what is missing. Changes nothing.",
		Annotations: readOnly,
	}, s.show)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "verify_object",
		Description: "Check that an object is in its desired state.",
		Annotations: readOnly,
	}, s.verify)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_recipes",
		Description: "List the recipes: repeatable setups such as running a service locally. Prefer a recipe over creating objects one by one.",
		Annotations: readOnly,
	}, s.listRecipes)

	if reg, err := s.recipes(); err == nil {
		addRecipePrompts(srv, reg)
	}

	if !cfg.ReadOnly {
		destructive := true
		mcp.AddTool(srv, &mcp.Tool{
			Name: "run_recipe",
			Description: "Run a recipe. Without approved it only returns the plan, with the slot bindings it proposes, " +
				"and status needs_input: show the plan to the user and let them approve or change the wiring. " +
				"Only call again with approved after the user said so. Existing objects are reused.",
		}, s.runRecipe)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "create_object",
			Description: "Create an object from field values and slot bindings. Does not apply it.",
		}, s.create)
		mcp.AddTool(srv, &mcp.Tool{
			Name: "apply_object",
			Description: "Bring an object and everything it uses into the desired state. " +
				"If input is missing the result has status needs_input: ask the user, never choose for them, " +
				"then call again with answers (field values) or use (slot bindings).",
		}, s.apply)
		mcp.AddTool(srv, &mcp.Tool{
			Name: "delete_object",
			Description: "Undo an object: run its destroy step, then remove it. " +
				"Set confirm only after the user has agreed to the deletion.",
			Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
		}, s.delete)
	}
	return srv
}

// Result is what every tool returns.
type Result struct {
	Status  string         `json:"status" jsonschema:"ok, failed, needs_input or needs_human"`
	Message string         `json:"message,omitempty"`
	Report  *engine.Report `json:"report,omitempty"`
	Needs   []prompt.Need  `json:"needs,omitempty"`
}

type objectInput struct {
	Object string `json:"object" jsonschema:"object reference as Kind/name"`
}

type createInput struct {
	Object string            `json:"object" jsonschema:"object reference as Kind/name"`
	Set    map[string]string `json:"set,omitempty" jsonschema:"field values to store on the object"`
	Use    map[string]string `json:"use,omitempty" jsonschema:"slot bindings as slot to Kind/name"`
}

type applyInput struct {
	Object  string            `json:"object" jsonschema:"object reference as Kind/name"`
	Flags   map[string]string `json:"flags,omitempty" jsonschema:"values for fields that accept a flag, for this run only"`
	Answers map[string]string `json:"answers,omitempty" jsonschema:"field values the user supplied; stored on the object"`
	Use     map[string]string `json:"use,omitempty" jsonschema:"slot bindings chosen by the user, as slot to Kind/name"`
	DryRun  bool              `json:"dryRun,omitempty" jsonschema:"only check what exists, change nothing"`
}

type deleteInput struct {
	Object  string `json:"object" jsonschema:"object reference as Kind/name"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"true only after the user agreed to delete"`
	DryRun  bool   `json:"dryRun,omitempty" jsonschema:"only report what would be deleted"`
}

type listObjectsInput struct {
	Kind string `json:"kind,omitempty" jsonschema:"limit the list to this kind"`
}

type kindInfo struct {
	Name   string            `json:"name"`
	Slots  map[string]string `json:"slots,omitempty"`
	Fields []fieldInfo       `json:"fields"`
}

type fieldInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

type kindsOutput struct {
	Kinds []kindInfo `json:"kinds"`
}

type objectsOutput struct {
	Objects []string `json:"objects"`
}

func (s *server) registry() (*kind.Registry, error) { return s.cfg.Kinds() }

func (s *server) engine(dryRun bool) (*engine.Engine, error) {
	reg, err := s.registry()
	if err != nil {
		return nil, err
	}
	return engine.New(engine.Config{
		Registry: reg,
		Store:    s.cfg.Store,
		Platform: s.cfg.Platform,
		DryRun:   dryRun,
		Stdout:   s.cfg.Log,
		Stderr:   s.cfg.Log,
		Log:      s.cfg.Log,
	}), nil
}

func (s *server) listKinds(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, kindsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, err := s.registry()
	if err != nil {
		return nil, kindsOutput{}, err
	}
	out := kindsOutput{Kinds: []kindInfo{}}
	for _, name := range reg.Names() {
		k, _ := reg.Get(name)
		info := kindInfo{Name: name, Slots: k.Uses}
		for _, f := range k.SortedFields() {
			info.Fields = append(info.Fields, fieldInfo{Name: f, Type: string(k.Fields[f].Type), Required: k.Fields[f].Required})
		}
		out.Kinds = append(out.Kinds, info)
	}
	return nil, out, nil
}

func (s *server) listObjects(_ context.Context, _ *mcp.CallToolRequest, in listObjectsInput) (*mcp.CallToolResult, objectsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, err := s.registry()
	if err != nil {
		return nil, objectsOutput{}, err
	}
	kinds := reg.Names()
	if in.Kind != "" {
		if _, ok := reg.Get(in.Kind); !ok {
			return nil, objectsOutput{}, fmt.Errorf("unknown kind %q", in.Kind)
		}
		kinds = []string{in.Kind}
	}
	out := objectsOutput{Objects: []string{}}
	for _, name := range kinds {
		refs, err := s.cfg.Store.List(name)
		if err != nil {
			return nil, objectsOutput{}, err
		}
		for _, ref := range refs {
			out.Objects = append(out.Objects, ref.String())
		}
	}
	return nil, out, nil
}

func (s *server) show(ctx context.Context, req *mcp.CallToolRequest, in objectInput) (*mcp.CallToolResult, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := object.ParseRef(in.Object)
	if err != nil {
		return nil, Result{}, err
	}
	eng, err := s.engine(false)
	if err != nil {
		return nil, Result{}, err
	}
	report, err := eng.Show(ctx, ref)
	return s.outcome(req, report, err, responses{})
}

func (s *server) verify(ctx context.Context, req *mcp.CallToolRequest, in objectInput) (*mcp.CallToolResult, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := object.ParseRef(in.Object)
	if err != nil {
		return nil, Result{}, err
	}
	eng, err := s.engine(false)
	if err != nil {
		return nil, Result{}, err
	}
	report, err := eng.Verify(ctx, ref)
	return s.outcome(req, report, err, responses{})
}

func (s *server) create(_ context.Context, _ *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := object.ParseRef(in.Object)
	if err != nil {
		return nil, Result{}, err
	}
	uses, err := parseUses(in.Use)
	if err != nil {
		return nil, Result{}, err
	}
	eng, err := s.engine(false)
	if err != nil {
		return nil, Result{}, err
	}
	if err := eng.Create(ref, in.Set, uses); err != nil {
		return nil, Result{}, err
	}
	return nil, Result{Status: StatusOK, Message: "created " + ref.String()}, nil
}

func (s *server) apply(ctx context.Context, req *mcp.CallToolRequest, in applyInput) (*mcp.CallToolResult, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := object.ParseRef(in.Object)
	if err != nil {
		return nil, Result{}, err
	}
	uses, err := parseUses(in.Use)
	if err != nil {
		return nil, Result{}, err
	}
	answered, err := collectResponses(req)
	if err != nil {
		return nil, Result{}, err
	}

	answers := mergeStrings(in.Answers, answered.fields)
	for slot, target := range answered.uses {
		uses[slot] = target
	}
	eng, err := s.engine(in.DryRun)
	if err != nil {
		return nil, Result{}, err
	}
	report, err := eng.Apply(ctx, ref, engine.Options{Flags: in.Flags, Answers: answers, Uses: uses})
	return s.outcome(req, report, err, answered)
}

func (s *server) delete(ctx context.Context, _ *mcp.CallToolRequest, in deleteInput) (*mcp.CallToolResult, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := object.ParseRef(in.Object)
	if err != nil {
		return nil, Result{}, err
	}
	if !in.Confirm && !in.DryRun {
		return nil, Result{
			Status:  StatusNeedsInput,
			Message: fmt.Sprintf("ask the user to confirm deleting %s, then call again with confirm set", ref),
		}, nil
	}
	eng, err := s.engine(in.DryRun)
	if err != nil {
		return nil, Result{}, err
	}
	actions, err := eng.Delete(ctx, ref)
	if err != nil {
		return s.outcome(nil, nil, err, responses{})
	}
	verb := "deleted"
	if in.DryRun {
		verb = "would delete"
	}
	return nil, Result{Status: StatusOK, Message: fmt.Sprintf("%s %s (%v)", verb, ref, actions)}, nil
}

// outcome turns an engine result into a tool result. Missing input and manual
// steps are normal outcomes, not tool errors.
func (s *server) outcome(req *mcp.CallToolRequest, report *engine.Report, err error, answered responses) (*mcp.CallToolResult, Result, error) {
	var needsInput *prompt.NeedsInputError
	var needsHuman *prompt.NeedsHumanError
	switch {
	case err == nil:
		return nil, Result{Status: StatusOK, Report: report}, nil
	case errors.As(err, &needsInput):
		if s.canElicit(req) && !answered.declined {
			return &mcp.CallToolResult{InputRequests: inputRequests(needsInput.Needs)}, Result{}, nil
		}
		return nil, Result{Status: StatusNeedsInput, Message: err.Error(), Needs: needsInput.Needs, Report: report}, nil
	case errors.As(err, &needsHuman):
		return nil, Result{Status: StatusNeedsHuman, Message: needsHuman.Message, Report: report}, nil
	case report != nil:
		return nil, Result{Status: StatusFailed, Message: err.Error(), Report: report}, nil
	default:
		return nil, Result{}, err
	}
}

func (s *server) canElicit(req *mcp.CallToolRequest) bool {
	if !s.cfg.Elicit || req == nil || req.Session == nil {
		return false
	}
	params := req.Session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

func parseUses(raw map[string]string) (map[string]object.Ref, error) {
	uses := make(map[string]object.Ref, len(raw))
	for slot, target := range raw {
		ref, err := object.ParseRef(target)
		if err != nil {
			return nil, err
		}
		uses[slot] = ref
	}
	return uses, nil
}

func mergeStrings(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
