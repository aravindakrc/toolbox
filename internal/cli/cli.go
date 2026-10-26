// Package cli implements the tbx command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/aravindakrc/toolbox/internal/engine"
	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/mcpserver"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

// Exit codes returned by Execute.
const (
	ExitOK         = 0
	ExitError      = 1
	ExitNeedsInput = 2
	ExitNeedsHuman = 3
)

type options struct {
	home       string
	kindDirs   []string
	recipeDirs []string
	noPrompt   bool
	output     string
}

// Execute runs tbx with args and returns the process exit code.
func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	root := newRoot(in, out, errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}

	var needsInput *prompt.NeedsInputError
	var needsHuman *prompt.NeedsHumanError
	var approval *recipe.ApprovalError
	fmt.Fprintln(errOut, "error:", err)
	switch {
	case errors.As(err, &needsInput), errors.As(err, &approval):
		return ExitNeedsInput
	case errors.As(err, &needsHuman):
		return ExitNeedsHuman
	default:
		return ExitError
	}
}

func newRoot(in io.Reader, out, errOut io.Writer) *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "tbx",
		Short:         "A toolbox of objects that set up and verify your environment",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)

	flags := root.PersistentFlags()
	flags.StringVar(&opts.home, "home", defaultHome(), "directory for objects, state and user kinds ($TBX_HOME)")
	flags.StringSliceVar(&opts.kindDirs, "kinds", nil, "extra directories to load kinds from")
	flags.StringSliceVar(&opts.recipeDirs, "recipes", nil, "extra directories to load recipes from")
	flags.BoolVar(&opts.noPrompt, "no-prompt", false, "never ask questions; report what is needed instead")
	flags.StringVarP(&opts.output, "output", "o", "text", "output format: text or json")

	app := &app{opts: opts, in: in, out: out, errOut: errOut}
	root.AddCommand(app.kindsCmd(), app.listCmd(), app.newCmd(), app.showCmd(), app.applyCmd(), app.verifyCmd(), app.deleteCmd(), app.recipeCmd(), app.mcpCmd())
	return root
}

func defaultHome() string {
	if home := os.Getenv("TBX_HOME"); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ".toolbox"
	}
	return filepath.Join(userHome, ".toolbox")
}

type app struct {
	opts   *options
	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

func (a *app) registry() (*kind.Registry, error) {
	dirs := append([]string{"kinds", filepath.Join(a.opts.home, "kinds")}, a.opts.kindDirs...)
	return kind.LoadRegistry(dirs...)
}

// recipes loads recipes from ./recipes, the home directory, --recipes and the
// directory next to each --kinds directory, so an MCP client that registers only
// --kinds still finds the recipes shipped beside them.
func (a *app) recipes() (*recipe.Registry, error) {
	dirs := []string{"recipes", filepath.Join(a.opts.home, "recipes")}
	for _, kindDir := range a.opts.kindDirs {
		dirs = append(dirs, filepath.Join(filepath.Dir(filepath.Clean(kindDir)), "recipes"))
	}
	return recipe.LoadRegistry(append(dirs, a.opts.recipeDirs...)...)
}
func (a *app) engine(dryRun bool) (*engine.Engine, error) {
	reg, err := a.registry()
	if err != nil {
		return nil, err
	}
	cfg := engine.Config{
		Registry: reg,
		Store:    object.NewStore(a.opts.home),
		Platform: platform.Current(),
		DryRun:   dryRun,
		Stdout:   a.errOut,
		Stderr:   a.errOut,
		Log:      a.errOut,
	}
	if !a.opts.noPrompt {
		cfg.Prompter = newTerminalPrompter(a.in, a.out)
	}
	return engine.New(cfg), nil
}

func (a *app) kindsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kinds",
		Short: "List the available kinds",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			reg, err := a.registry()
			if err != nil {
				return err
			}
			for _, name := range reg.Names() {
				fmt.Fprintln(a.out, name)
			}
			return nil
		},
	}
}

func (a *app) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [Kind]",
		Short: "List the objects, optionally of one kind",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := a.registry()
			if err != nil {
				return err
			}
			kinds := reg.Names()
			if len(args) == 1 {
				kinds = args
			}
			store := object.NewStore(a.opts.home)
			refs := []string{}
			for _, k := range kinds {
				found, err := store.List(k)
				if err != nil {
					return err
				}
				for _, ref := range found {
					refs = append(refs, ref.String())
				}
			}
			if a.opts.output == "json" {
				return json.NewEncoder(a.out).Encode(refs)
			}
			for _, ref := range refs {
				fmt.Fprintln(a.out, ref)
			}
			return nil
		},
	}
}

func (a *app) newCmd() *cobra.Command {
	var set, uses []string
	cmd := &cobra.Command{
		Use:   "new Kind/name",
		Short: "Create an object",
		Example: "  tbx new GithubRepo/toolbox --set org=aravindakrc --set name=toolbox\n" +
			"  tbx new LocalRepo/toolbox --use repo=GithubRepo/toolbox",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ref, err := object.ParseRef(args[0])
			if err != nil {
				return err
			}
			values, err := parsePairs(set)
			if err != nil {
				return err
			}
			bindings, err := parseBindings(uses)
			if err != nil {
				return err
			}
			eng, err := a.engine(false)
			if err != nil {
				return err
			}
			if err := eng.Create(ref, values, bindings); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "created", ref)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&set, "set", nil, "store a field value, as field=value")
	cmd.Flags().StringArrayVar(&uses, "use", nil, "bind a slot, as slot=Kind/name")
	return cmd
}

func (a *app) showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show Kind/name",
		Short: "Show an object's fields and where they come from",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ref, err := object.ParseRef(args[0])
			if err != nil {
				return err
			}
			eng, err := a.engine(false)
			if err != nil {
				return err
			}
			report, err := eng.Show(context.Background(), ref)
			if err != nil {
				return err
			}
			return printReport(a.out, report, a.opts.output)
		},
	}
}

func (a *app) applyCmd() *cobra.Command {
	var set, uses []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply Kind/name",
		Short: "Complete an object and everything it uses",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := object.ParseRef(args[0])
			if err != nil {
				return err
			}
			flags, err := parsePairs(set)
			if err != nil {
				return err
			}
			bindings, err := parseBindings(uses)
			if err != nil {
				return err
			}
			eng, err := a.engine(dryRun)
			if err != nil {
				return err
			}
			report, err := eng.Apply(cmd.Context(), ref, engine.Options{Flags: flags, Uses: bindings})
			if err != nil {
				return err
			}
			return printReport(a.out, report, a.opts.output)
		},
	}
	cmd.Flags().StringArrayVar(&set, "set", nil, "supply a flag value for this run, as name=value")
	cmd.Flags().StringArrayVar(&uses, "use", nil, "bind a slot for this run, as slot=Kind/name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check what exists without changing anything")
	return cmd
}

func (a *app) verifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify Kind/name",
		Short: "Check that an object is in its desired state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := object.ParseRef(args[0])
			if err != nil {
				return err
			}
			eng, err := a.engine(false)
			if err != nil {
				return err
			}
			report, err := eng.Verify(cmd.Context(), ref)
			if report != nil {
				if printErr := printReport(a.out, report, a.opts.output); printErr != nil {
					return printErr
				}
			}
			return err
		},
	}
}

func (a *app) deleteCmd() *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "delete Kind/name",
		Short: "Undo an object: run its destroy step, then remove it and its state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := object.ParseRef(args[0])
			if err != nil {
				return err
			}
			eng, err := a.engine(dryRun)
			if err != nil {
				return err
			}
			if !dryRun && !yes {
				if err := a.confirmDelete(cmd.Context(), ref); err != nil {
					return err
				}
			}
			actions, err := eng.Delete(cmd.Context(), ref)
			if err != nil {
				return err
			}
			prefix := "deleted"
			if dryRun {
				prefix = "would delete"
			}
			fmt.Fprintf(a.out, "%s %s (%s)\n", prefix, ref, strings.Join(actions, ", "))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without changing anything")
	return cmd
}

func (a *app) confirmDelete(ctx context.Context, ref object.Ref) error {
	if a.opts.noPrompt {
		return errors.New("confirmation required: pass --yes to delete without asking")
	}
	answer, err := newTerminalPrompter(a.in, a.out).Choose(ctx, fmt.Sprintf("Delete %s?", ref), []string{"no", "yes"})
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

func (a *app) mcpCmd() *cobra.Command {
	var readOnly, elicit bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the toolbox to agents over MCP on stdin and stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv := mcpserver.New(mcpserver.Config{
				Kinds:    a.registry,
				Store:    object.NewStore(a.opts.home),
				Recipes:  a.recipes,
				Platform: platform.Current(),
				Log:      a.errOut,
				ReadOnly: readOnly,
				Elicit:   elicit,
			})
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "only offer tools that do not change anything")
	cmd.Flags().BoolVar(&elicit, "elicit", false, "ask the client to collect missing input from the user")
	return cmd
}

func parsePairs(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid value %q, expected name=value", pair)
		}
		out[key] = value
	}
	return out, nil
}

func parseBindings(pairs []string) (map[string]object.Ref, error) {
	raw, err := parsePairs(pairs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]object.Ref, len(raw))
	for slot, target := range raw {
		ref, err := object.ParseRef(target)
		if err != nil {
			return nil, err
		}
		out[slot] = ref
	}
	return out, nil
}
