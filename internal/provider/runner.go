// Package provider runs the scripts and manual steps that manage an object.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
)

// outputVar names the environment variable holding the path of the file a
// script writes key=value outputs to.
const outputVar = "TBX_OUT"

// Runner executes provider operations.
type Runner struct {
	Platform platform.Info
	Stdout   io.Writer
	Stderr   io.Writer
	// Prompter confirms manual steps. When nil, a manual step is reported as
	// a *prompt.NeedsHumanError.
	Prompter prompt.Prompter
	// Dir is the working directory for scripts. Empty means the current one.
	Dir string
}

// Result is the outcome of running an operation.
type Result struct {
	// Defined is false when the kind has no such operation.
	Defined  bool
	ExitCode int
	// Outputs are the key=value lines the script wrote to $TBX_OUT.
	Outputs map[string]string
}

// OK reports whether the operation was defined and succeeded.
func (r Result) OK() bool { return r.Defined && r.ExitCode == 0 }

// Run performs the first step of op that matches the platform. Field values
// are passed to the script as environment variables in env.
func (r *Runner) Run(ctx context.Context, k *kind.Kind, op kind.Operation, env map[string]string) (Result, error) {
	if len(op) == 0 {
		return Result{}, nil
	}
	step, ok := r.selectStep(op)
	if !ok {
		return Result{}, fmt.Errorf("kind %s has no step for %s/%s", k.Name, r.Platform.OS, r.Platform.Arch)
	}

	if step.Manual != "" {
		return Result{Defined: true}, r.manual(ctx, k, step.Manual, env)
	}

	script, err := scriptText(k, step)
	if err != nil {
		return Result{}, err
	}
	outFile, err := os.CreateTemp("", "tbx-out-*")
	if err != nil {
		return Result{}, err
	}
	outPath := outFile.Name()
	outFile.Close()
	defer os.Remove(outPath)

	vars := make(map[string]string, len(env)+1)
	for name, v := range env {
		vars[name] = v
	}
	vars[outputVar] = outPath

	code, err := r.execute(ctx, step.Shell, script, vars, r.Stdout, r.Stderr)
	if err != nil {
		return Result{}, err
	}
	outputs, err := readOutputs(outPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Defined: true, ExitCode: code, Outputs: outputs}, nil
}

// Output runs a script in the portable shell and returns its standard output.
func (r *Runner) Output(ctx context.Context, script string) (string, error) {
	var out bytes.Buffer
	code, err := r.execute(ctx, kind.ShellPortable, script, nil, &out, io.Discard)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out.String(), fmt.Errorf("command exited with status %d", code)
	}
	return out.String(), nil
}

func (r *Runner) selectStep(op kind.Operation) (kind.Step, bool) {
	for _, s := range op {
		if s.When.Matches(r.Platform) {
			return s, true
		}
	}
	return kind.Step{}, false
}

// manual shows the Markdown instructions in doc, with ${field} placeholders
// replaced by the object's values, and waits for the user to finish.
func (r *Runner) manual(ctx context.Context, k *kind.Kind, doc string, env map[string]string) error {
	path, err := insideKind(k, doc)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("kind %s: manual step: %w", k.Name, err)
	}
	msg := fillPlaceholders(string(data), env)
	if r.Prompter == nil {
		return &prompt.NeedsHumanError{Message: msg}
	}
	return r.Prompter.Acknowledge(ctx, msg)
}

func scriptText(k *kind.Kind, step kind.Step) (string, error) {
	if step.File == "" {
		return step.Run, nil
	}
	path, err := insideKind(k, step.File)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// insideKind resolves rel against the kind's directory and rejects paths that escape it.
func insideKind(k *kind.Kind, rel string) (string, error) {
	path := filepath.Join(k.Dir, rel)
	back, err := filepath.Rel(k.Dir, path)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("kind %s: %q is outside the kind directory", k.Name, rel)
	}
	return path, nil
}

func (r *Runner) execute(ctx context.Context, shell, script string, vars map[string]string, stdout, stderr io.Writer) (int, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	dir := r.Dir
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return 0, err
		}
	}
	environ := os.Environ()
	for name, v := range vars {
		environ = append(environ, name+"="+v)
	}
	if shell == kind.ShellPortable {
		return r.runPortable(ctx, dir, script, environ, stdout, stderr)
	}
	return r.runExternal(ctx, dir, shell, script, environ, stdout, stderr)
}

func (r *Runner) runPortable(ctx context.Context, dir, script string, environ []string, stdout, stderr io.Writer) (int, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return 0, fmt.Errorf("parse script: %w", err)
	}
	runner, err := interp.New(
		interp.Dir(dir),
		interp.Env(expand.ListEnviron(environ...)),
		interp.StdIO(nil, stdout, stderr),
		interp.ExecHandlers(builtins),
	)
	if err != nil {
		return 0, err
	}
	err = runner.Run(ctx, file)
	var status interp.ExitStatus
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &status):
		return int(status), nil
	default:
		return 0, err
	}
}

func (r *Runner) runExternal(ctx context.Context, dir, shell, script string, environ []string, stdout, stderr io.Writer) (int, error) {
	var args []string
	switch shell {
	case kind.ShellBash:
		args = []string{"bash", "-c", script}
	case kind.ShellCmd:
		args = []string{"cmd", "/C", script}
	case kind.ShellPowerShell:
		bin := "powershell"
		if r.Platform.OS != "windows" {
			bin = "pwsh"
		}
		args = []string{bin, "-NoProfile", "-NonInteractive", "-Command", script}
	default:
		return 0, fmt.Errorf("unsupported shell %q", shell)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = environ
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	default:
		return 0, fmt.Errorf("run %s: %w", args[0], err)
	}
}

// readOutputs parses key=value lines, ignoring blank lines and # comments.
func readOutputs(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	outputs := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid output line %q, expected key=value", line)
		}
		outputs[strings.TrimSpace(key)] = value
	}
	return outputs, scanner.Err()
}

var placeholder = regexp.MustCompile(`\$\{([A-Za-z][A-Za-z0-9]*)\}`)

// fillPlaceholders replaces ${field} with the field's value from env and
// leaves unknown placeholders as they are.
func fillPlaceholders(text string, env map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		if v, ok := env["TBX_"+EnvName(m[2:len(m)-1])]; ok {
			return v
		}
		return m
	})
}

// EnvName converts a field name such as cloneUrl to the variable name
// suffix CLONE_URL.
func EnvName(name string) string {
	var b strings.Builder
	for i, c := range name {
		if i > 0 && c >= 'A' && c <= 'Z' {
			if prev := name[i-1]; prev < 'A' || prev > 'Z' {
				b.WriteByte('_')
			}
		}
		b.WriteRune(c)
	}
	return strings.ToUpper(b.String())
}

// Env builds the environment for a script: TBX_<FIELD> for the object's
// fields and TBX_<SLOT>_<FIELD> for the values of the objects it uses.
func Env(fields map[string]string, slots map[string]map[string]string) map[string]string {
	env := make(map[string]string, len(fields))
	for name, v := range fields {
		env["TBX_"+EnvName(name)] = v
	}
	slotNames := make([]string, 0, len(slots))
	for slot := range slots {
		slotNames = append(slotNames, slot)
	}
	sort.Strings(slotNames)
	for _, slot := range slotNames {
		for name, v := range slots[slot] {
			env["TBX_"+EnvName(slot)+"_"+EnvName(name)] = v
		}
	}
	return env
}
