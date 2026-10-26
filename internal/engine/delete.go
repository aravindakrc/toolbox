package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/prompt"
)

// Dependents returns the objects that are bound to ref.
func (e *Engine) Dependents(ref object.Ref) ([]object.Ref, error) {
	var found []object.Ref
	for _, name := range e.cfg.Registry.Names() {
		refs, err := e.cfg.Store.List(name)
		if err != nil {
			return nil, err
		}
		for _, other := range refs {
			obj, err := e.cfg.Store.Load(other)
			if err != nil {
				return nil, err
			}
			for _, target := range obj.Uses {
				if target == ref {
					found = append(found, other)
					break
				}
			}
		}
	}
	return found, nil
}

// Delete runs the kind's destroy step, if it has one, and then removes the
// object and its status. It refuses while other objects still use ref. With
// DryRun set it only reports what would happen.
func (e *Engine) Delete(ctx context.Context, ref object.Ref) ([]string, error) {
	dependents, err := e.Dependents(ref)
	if err != nil {
		return nil, err
	}
	if len(dependents) > 0 {
		return nil, fmt.Errorf("%s is still used by %s; delete those first", ref, joinRefs(dependents))
	}

	p, err := e.resolve(ctx, ref, Options{}, false, e.storedValues)
	var needs *prompt.NeedsInputError
	if err != nil && !errors.As(err, &needs) {
		return nil, err
	}

	hasDestroy := len(p.kind.Provider.Destroy) > 0
	if hasDestroy && needs != nil {
		return nil, needs
	}
	if hasDestroy && len(p.result.Missing) > 0 {
		return nil, &prompt.NeedsInputError{Needs: p.result.Missing}
	}

	var actions []string
	if hasDestroy {
		actions = append(actions, "destroy")
		if !e.cfg.DryRun {
			env := e.env(p)
			e.logf("%s: destroying", ref)
			res, err := e.runner.Run(ctx, p.kind, p.kind.Provider.Destroy, env)
			if err != nil {
				return nil, fmt.Errorf("%s: destroy: %w", ref, err)
			}
			if res.ExitCode != 0 {
				return nil, fmt.Errorf("%s: destroy failed with exit status %d; the object was kept", ref, res.ExitCode)
			}
		}
	}
	actions = append(actions, "remove object and state")
	if !e.cfg.DryRun {
		if err := e.cfg.Store.Delete(ref); err != nil {
			return nil, err
		}
	}
	return actions, nil
}
