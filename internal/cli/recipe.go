package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

func (a *app) recipeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipe",
		Short: "List and run recipes: repeatable setups made of several objects",
	}
	cmd.AddCommand(a.recipeListCmd(), a.recipeRunCmd())
	return cmd
}

func (a *app) recipeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the available recipes",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			reg, err := a.recipes()
			if err != nil {
				return err
			}
			for _, name := range reg.Names() {
				r, _ := reg.Get(name)
				fmt.Fprintf(a.out, "%s\n  %s\n", name, strings.Join(strings.Fields(r.Description), " "))
				for _, in := range r.Inputs {
					fmt.Fprintf(a.out, "  --input %s=...  %s\n", in.Name, inputNote(in))
				}
			}
			return nil
		},
	}
}

func inputNote(in recipe.Input) string {
	switch {
	case in.Default != "":
		return fmt.Sprintf("%s (default %s)", in.Description, in.Default)
	case in.Optional:
		return in.Description + " (optional)"
	}
	return in.Description
}

func (a *app) recipeRunCmd() *cobra.Command {
	var inputs, bindings []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "run NAME",
		Short: "Show what a recipe would do, and after your approval do it",
		Example: "  tbx recipe run local-service --input service=market-data-service \\\n" +
			"      --input cluster=activate-us-development-aks --input namespace=my-dod-namespace",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := a.recipes()
			if err != nil {
				return err
			}
			rec, ok := reg.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown recipe %q", args[0])
			}
			values, err := parsePairs(inputs)
			if err != nil {
				return err
			}
			rebind, err := parsePairs(bindings)
			if err != nil {
				return err
			}
			eng, err := a.engine(false)
			if err != nil {
				return err
			}
			runner := &recipe.Runner{Engine: eng, Store: object.NewStore(a.opts.home)}
			return a.runRecipe(cmd.Context(), runner, recipe.Request{Recipe: rec, Inputs: values, Rebind: rebind, Approved: yes})
		},
	}
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "a recipe input, as name=value")
	cmd.Flags().StringArrayVar(&bindings, "use", nil, "bind a slot differently from the plan, as Kind/name.slot=Kind/name")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "approve the planned wiring without asking")
	return cmd
}

// runRecipe runs the request, asking for missing inputs and for approval
// unless the session is non-interactive.
func (a *app) runRecipe(ctx context.Context, runner *recipe.Runner, req recipe.Request) error {
	for {
		result, err := runner.Run(ctx, req)

		var needsInput *prompt.NeedsInputError
		var approval *recipe.ApprovalError
		switch {
		case errors.As(err, &needsInput) && !a.opts.noPrompt && result == nil:
			if err := a.askInputs(ctx, &req, needsInput.Needs); err != nil {
				return err
			}
			continue
		case errors.As(err, &approval):
			if err := a.printPlan(approval.Plan); err != nil {
				return err
			}
			if a.opts.noPrompt {
				return err
			}
			answer, askErr := newTerminalPrompter(a.in, a.out).Choose(ctx, "Create these objects with this wiring?", []string{"no", "yes"})
			if askErr != nil {
				return askErr
			}
			if answer != "yes" {
				return errors.New("cancelled")
			}
			req.Approved = true
			continue
		}

		if result != nil {
			for _, report := range result.Reports {
				if printErr := printReport(a.out, report, a.opts.output); printErr != nil {
					return printErr
				}
			}
		}
		return err
	}
}

func (a *app) askInputs(ctx context.Context, req *recipe.Request, needs []prompt.Need) error {
	if req.Inputs == nil {
		req.Inputs = map[string]string{}
	}
	prompter := newTerminalPrompter(a.in, a.out)
	for _, n := range needs {
		value, err := prompter.Ask(ctx, n.Message)
		if err != nil {
			return err
		}
		req.Inputs[n.Name] = value
	}
	return nil
}

func (a *app) printPlan(plan recipe.Plan) error {
	return writePlan(a.out, plan, a.opts.output)
}

func writePlan(w io.Writer, plan recipe.Plan, format string) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}
	fmt.Fprintf(w, "recipe %s\n", plan.Recipe)
	for _, s := range plan.Steps {
		fmt.Fprintf(w, "  %-7s %s\n", s.Action, s.Object)
		if s.Action != recipe.ActionCreate {
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(s.Set)) {
			fmt.Fprintf(w, "            set %s = %s\n", k, s.Set[k])
		}
		for _, k := range slices.Sorted(maps.Keys(s.Use)) {
			fmt.Fprintf(w, "            use %s -> %s\n", k, s.Use[k])
		}
	}
	return nil
}
