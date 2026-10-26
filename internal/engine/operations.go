package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"os"
	"strings"
)

// Show reports an object's fields, where each value came from, and what is
// still missing. It never prompts and never runs a provider.
func (e *Engine) Show(ctx context.Context, ref object.Ref) (*Report, error) {
	p, err := e.resolve(ctx, ref, Options{}, false, e.storedValues)
	var needs *prompt.NeedsInputError
	if err != nil && !errors.As(err, &needs) {
		return nil, err
	}
	stored, err := e.storedStatus(ref)
	if err != nil {
		return nil, err
	}
	report := e.report(p, stored, nil)
	if needs != nil {
		report.Needs = append(needs.Needs, report.Needs...)
		report.Complete = false
	}
	return report, nil
}

// Apply brings an object, and everything it uses, into the desired state.
func (e *Engine) Apply(ctx context.Context, ref object.Ref, opts Options) (*Report, error) {
	status, p, actions, err := e.apply(ctx, ref, opts, nil)
	if err != nil {
		return nil, err
	}
	return e.report(p, status, actions), nil
}

func (e *Engine) apply(ctx context.Context, ref object.Ref, opts Options, stack []object.Ref) (*object.Status, *prepared, []string, error) {
	if i := indexOf(stack, ref); i >= 0 {
		return nil, nil, nil, fmt.Errorf("dependency cycle: %s", joinRefs(append(stack[i:], ref)))
	}
	path := append(stack, ref)

	dep := func(target object.Ref) (map[string]string, error) {
		st, _, _, err := e.apply(ctx, target, Options{}, path)
		if err != nil {
			return nil, err
		}
		return st.Values(), nil
	}
	p, err := e.resolve(ctx, ref, opts, true, dep)
	if err != nil {
		e.keepProgress(p, err)
		return nil, nil, nil, err
	}
	if len(p.result.Missing) > 0 {
		err := &prompt.NeedsInputError{Needs: p.result.Missing}
		e.keepProgress(p, err)
		return nil, nil, nil, err
	}
	if p.changed && !e.cfg.DryRun {
		if err := e.cfg.Store.Save(p.object); err != nil {
			return nil, nil, nil, err
		}
	}

	prev, err := e.storedStatus(ref)
	if err != nil {
		return nil, nil, nil, err
	}
	status := e.newStatus(p, prev)
	env := e.env(p)
	var actions []string

	if !p.kind.Provider.Defined() {
		if !e.cfg.DryRun {
			if err := e.cfg.Store.SaveStatus(status); err != nil {
				return nil, nil, nil, err
			}
		}
		return status, p, []string{"no provider; fields only"}, nil
	}

	e.logf("%s: checking", ref)
	found, err := e.runner.Run(ctx, p.kind, p.kind.Provider.Discover, env)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: discover: %w", ref, err)
	}
	merge(status.Outputs, found.Outputs)

	// A kind without a discover step, such as a manual one, counts as present
	// once it has been applied.
	present := found.OK() || (!found.Defined && prev != nil && prev.AppliedAt != nil)

	switch {
	case present:
		actions = append(actions, "already present")
	case e.cfg.DryRun:
		return status, p, append(actions, "would apply"), nil
	default:
		if len(p.kind.Provider.Apply) == 0 {
			return nil, nil, nil, fmt.Errorf("%s does not exist and kind %s has no apply step", ref, p.kind.Name)
		}
		if err := os.MkdirAll(env["TBX_DATA_DIR"], 0o755); err != nil {
			return nil, nil, nil, err
		}
		e.logf("%s: applying", ref)
		applied, err := e.runner.Run(ctx, p.kind, p.kind.Provider.Apply, env)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: apply: %w", ref, err)
		}
		if applied.ExitCode != 0 {
			return nil, nil, nil, fmt.Errorf("%s: apply failed with exit status %d", ref, applied.ExitCode)
		}
		merge(status.Outputs, applied.Outputs)
		actions = append(actions, "applied")
		status.AppliedAt = nil
	}
	if status.AppliedAt == nil {
		now := e.now()
		status.AppliedAt = &now
	}

	if !e.cfg.DryRun {
		if err := e.checkVerify(ctx, p, env, status); err != nil {
			return nil, nil, nil, err
		}
	}
	if e.cfg.DryRun {
		return status, p, actions, nil
	}
	if err := e.cfg.Store.SaveStatus(status); err != nil {
		return nil, nil, nil, err
	}
	return status, p, actions, nil
}

// keepProgress saves bindings and answers gathered so far when a run stops to
// ask for more input, so they do not have to be given again.
func (e *Engine) keepProgress(p *prepared, err error) {
	var needs *prompt.NeedsInputError
	if p != nil && p.changed && !e.cfg.DryRun && errors.As(err, &needs) {
		_ = e.cfg.Store.Save(p.object)
	}
}

// Verify runs an object's verify operation against its last resolved values.
func (e *Engine) Verify(ctx context.Context, ref object.Ref) (*Report, error) {
	p, err := e.resolve(ctx, ref, Options{}, false, e.storedValues)
	if err != nil {
		return nil, err
	}
	if len(p.result.Missing) > 0 {
		return nil, &prompt.NeedsInputError{Needs: p.result.Missing}
	}
	if len(p.kind.Provider.Verify) == 0 {
		return nil, fmt.Errorf("kind %s has no verify step", p.kind.Name)
	}

	prev, err := e.storedStatus(ref)
	if err != nil {
		return nil, err
	}
	status := e.newStatus(p, prev)
	env := e.env(p)
	verifyErr := e.checkVerify(ctx, p, env, status)
	if saveErr := e.cfg.Store.SaveStatus(status); saveErr != nil {
		return nil, saveErr
	}
	report := e.report(p, status, nil)
	return report, verifyErr
}

// checkVerify runs the verify operation, records the outcome on status and
// returns an error if verification failed.
func (e *Engine) checkVerify(ctx context.Context, p *prepared, env map[string]string, status *object.Status) error {
	if len(p.kind.Provider.Verify) == 0 {
		return nil
	}
	res, err := e.runner.Run(ctx, p.kind, p.kind.Provider.Verify, env)
	if err != nil {
		return fmt.Errorf("%s: verify: %w", p.object.Ref, err)
	}
	merge(status.Outputs, res.Outputs)
	status.LastVerify = &object.Verification{OK: res.ExitCode == 0, At: e.now()}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: verification failed with exit status %d", p.object.Ref, res.ExitCode)
	}
	return nil
}

// newStatus starts a status from the resolved fields, keeping outputs from
// earlier runs because discovery does not always repeat them.
func (e *Engine) newStatus(p *prepared, prev *object.Status) *object.Status {
	status := &object.Status{
		Ref:      p.object.Ref,
		Resolved: p.result.Fields,
		Outputs:  map[string]string{},
	}
	if prev != nil {
		merge(status.Outputs, prev.Outputs)
		status.AppliedAt = prev.AppliedAt
	}
	return status
}

func (e *Engine) storedStatus(ref object.Ref) (*object.Status, error) {
	st, err := e.cfg.Store.LoadStatus(ref)
	if errors.Is(err, object.ErrNotFound) {
		return nil, nil
	}
	return st, err
}

func (e *Engine) report(p *prepared, status *object.Status, actions []string) *Report {
	r := &Report{Object: p.object.Ref.String(), Actions: actions, Complete: len(p.result.Missing) == 0}
	r.Needs = p.result.Missing

	for _, name := range p.kind.SortedFields() {
		f := FieldReport{Name: name}
		if v, ok := p.result.Fields[name]; ok {
			f.Value, f.From = v.Value, v.From
		} else if p.kind.Fields[name].Required {
			f.Missing = true
		}
		r.Fields = append(r.Fields, f)
	}
	if len(p.object.Uses) > 0 {
		r.Uses = make(map[string]string, len(p.object.Uses))
		for slot, target := range p.object.Uses {
			r.Uses[slot] = target.String()
		}
	}
	if status != nil {
		r.Outputs = status.Outputs
		if v := status.LastVerify; v != nil {
			r.Verified = &v.OK
		}
	}
	return r
}

func merge(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

func indexOf(refs []object.Ref, ref object.Ref) int {
	for i, r := range refs {
		if r == ref {
			return i
		}
	}
	return -1
}

func joinRefs(refs []object.Ref) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = r.String()
	}
	return strings.Join(parts, " -> ")
}
