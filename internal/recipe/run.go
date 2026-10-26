package recipe

import (
	"context"
	"fmt"

	"github.com/aravindakrc/toolbox/internal/engine"
	"github.com/aravindakrc/toolbox/internal/object"
)

// Runner runs recipes with an engine.
type Runner struct {
	Engine *engine.Engine
	Store  *object.Store
}

// Request is one run of a recipe.
type Request struct {
	Recipe *Recipe
	Inputs map[string]string
	// Rebind replaces proposed bindings, keyed as "Kind/name.slot".
	Rebind map[string]string
	// Approved is set once the user has seen the plan and accepted its wiring.
	Approved bool
}

// Result is the outcome of a run.
type Result struct {
	Plan Plan `json:"plan"`
	// Reports are the applied objects, in order, up to the first stop.
	Reports []*engine.Report `json:"reports,omitempty"`
}

// ApprovalError reports that the plan creates objects and binds slots, which
// only the user may approve.
type ApprovalError struct {
	Plan Plan
}

func (e *ApprovalError) Error() string {
	return fmt.Sprintf("approval required: show the plan of recipe %s to the user and run again once they approve", e.Plan.Recipe)
}

// Run plans the recipe, and once approved creates the missing objects and
// applies every object in order. When the plan only reuses objects there is
// nothing to approve, so a run stopped by a manual step can simply be repeated.
// The result is returned together with any error, to show how far it got.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	plan, err := BuildPlan(req.Recipe, req.Inputs, req.Rebind, r.Store.Exists)
	if err != nil {
		return nil, err
	}
	result := &Result{Plan: plan}
	if plan.NeedsApproval() && !req.Approved {
		return result, &ApprovalError{Plan: plan}
	}

	refs := make([]object.Ref, len(plan.Steps))
	for i, step := range plan.Steps {
		ref, err := object.ParseRef(step.Object)
		if err != nil {
			return result, err
		}
		refs[i] = ref
		if step.Action != ActionCreate {
			// A run that stopped early leaves objects without the fields it
			// had not reached; complete them without touching chosen values.
			if err := r.Engine.Fill(ref, step.Set); err != nil {
				return result, fmt.Errorf("%s: %w", ref, err)
			}
			continue
		}
		uses, err := parseUses(step.Use)
		if err != nil {
			return result, fmt.Errorf("%s: %w", ref, err)
		}
		if err := r.Engine.Create(ref, step.Set, uses); err != nil {
			return result, err
		}
	}

	for _, ref := range refs {
		report, err := r.Engine.Apply(ctx, ref, engine.Options{})
		if report != nil {
			result.Reports = append(result.Reports, report)
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
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
