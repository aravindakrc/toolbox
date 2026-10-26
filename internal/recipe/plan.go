package recipe

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/prompt"
)

// Actions a planned step can take.
const (
	ActionCreate = "create"
	ActionReuse  = "reuse"
)

// PlannedStep is a step with its inputs filled in.
type PlannedStep struct {
	Object string            `json:"object"`
	Action string            `json:"action"`
	Set    map[string]string `json:"set,omitempty"`
	Use    map[string]string `json:"use,omitempty"`
}

// Plan is what running a recipe would do. An existing object is reused as it
// is; only new objects get the proposed fields and bindings.
type Plan struct {
	Recipe string        `json:"recipe"`
	Steps  []PlannedStep `json:"steps"`
}

// NeedsApproval reports whether the plan creates objects, and so binds slots.
func (p Plan) NeedsApproval() bool {
	for _, s := range p.Steps {
		if s.Action == ActionCreate {
			return true
		}
	}
	return false
}

var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// BuildPlan fills in the recipe's inputs and decides, per step, whether the
// object is created or reused. rebind replaces proposed bindings, keyed as
// "Kind/name.slot", with a binding the user chose.
func BuildPlan(r *Recipe, inputs, rebind map[string]string, exists func(object.Ref) bool) (Plan, error) {
	values, err := resolveInputs(r, inputs)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Recipe: r.Name}
	used := map[string]bool{}
	for _, s := range r.Steps {
		name, err := expand(s.Object, values)
		if err != nil {
			return Plan{}, err
		}
		ref, err := object.ParseRef(name)
		if err != nil {
			return Plan{}, fmt.Errorf("recipe %s: %w", r.Name, err)
		}
		step := PlannedStep{Object: ref.String(), Action: ActionCreate}
		if exists(ref) {
			step.Action = ActionReuse
		}
		if step.Set, err = expandMap(s.Set, values, true); err != nil {
			return Plan{}, err
		}
		if step.Use, err = expandMap(s.Use, values, false); err != nil {
			return Plan{}, err
		}
		for slot := range step.Use {
			key := ref.String() + "." + slot
			if target, ok := rebind[key]; ok {
				step.Use[slot] = target
				used[key] = true
			}
		}
		plan.Steps = append(plan.Steps, step)
	}

	for key := range rebind {
		if !used[key] {
			return Plan{}, fmt.Errorf("binding %q does not match any slot in the plan", key)
		}
	}
	return plan, nil
}

// resolveInputs checks the supplied inputs and fills defaults. Missing
// required inputs are reported as needed input.
func resolveInputs(r *Recipe, supplied map[string]string) (map[string]string, error) {
	known := make(map[string]bool, len(r.Inputs))
	for _, in := range r.Inputs {
		known[in.Name] = true
	}
	for name := range supplied {
		if !known[name] {
			return nil, fmt.Errorf("recipe %s has no input %q", r.Name, name)
		}
	}

	values := map[string]string{}
	var needs []prompt.Need
	for _, in := range r.Inputs {
		value := strings.TrimSpace(supplied[in.Name])
		if value == "" && in.Default != "" {
			var err error
			if value, err = expand(in.Default, values); err != nil {
				return nil, fmt.Errorf("input %q: %w", in.Name, err)
			}
		}
		if value == "" && in.Required() {
			needs = append(needs, prompt.Need{Name: in.Name, Type: prompt.NeedField, Message: in.Description})
		}
		values[in.Name] = value
	}
	if len(needs) > 0 {
		return nil, &prompt.NeedsInputError{Needs: needs}
	}
	return values, nil
}

func expand(text string, values map[string]string) (string, error) {
	var missing []string
	out := variable.ReplaceAllStringFunc(text, func(m string) string {
		v, ok := values[m[2:len(m)-1]]
		if !ok {
			missing = append(missing, m)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unknown input %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// expandMap expands every value. With skipEmpty, entries that end up empty
// (an optional input that was not given) are left out.
func expandMap(in, values map[string]string, skipEmpty bool) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, err := expand(in[k], values)
		if err != nil {
			return nil, err
		}
		if v == "" && skipEmpty {
			continue
		}
		out[k] = v
	}
	return out, nil
}
