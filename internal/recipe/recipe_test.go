package recipe_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aravindakrc/toolbox/internal/engine"
	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

const sourceKind = `
kind: Source
fields:
  value:
    type: string
    required: true
    sources:
      - flag: value
`

const fileKind = `
kind: File
uses:
  source: Source
fields:
  path:
    type: string
    required: true
    sources:
      - flag: path
  content:
    type: string
    sources:
      - ref: source.value
provider:
  discover: test -f "$TBX_PATH"
  apply: echo "$TBX_CONTENT" > "$TBX_PATH"
  verify: test -f "$TBX_PATH"
`

const recipeYAML = `
recipe: write-file
description: Write a file
inputs:
  - name: name
    description: Name of the file
  - name: value
    description: Text to write
    default: hello-${name}
  - name: note
    description: Optional note
    optional: true
steps:
  - object: Source/${name}
    set:
      value: ${value}
  - object: File/${name}
    set:
      content: ${note}
    use:
      source: Source/${name}
`

func load(t *testing.T, text string) *recipe.Recipe {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recipe.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := recipe.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func none(object.Ref) bool { return false }

func TestPlanFillsDefaultsAndSkipsEmptyOptionalFields(t *testing.T) {
	r := load(t, `
recipe: r
inputs:
  - name: name
  - name: value
    default: hello-${name}
  - name: note
    optional: true
steps:
  - object: Source/${name}
    set:
      value: ${value}
      note: ${note}
`)
	plan, err := recipe.BuildPlan(r, map[string]string{"name": "a"}, nil, none)
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if step.Object != "Source/a" || step.Action != recipe.ActionCreate {
		t.Fatalf("step = %+v", step)
	}
	if step.Set["value"] != "hello-a" {
		t.Errorf("value = %q, want hello-a", step.Set["value"])
	}
	if _, ok := step.Set["note"]; ok {
		t.Error("an optional input that was not given must not be set")
	}
}

func TestPlanReportsMissingInputs(t *testing.T) {
	r := load(t, recipeYAML)
	_, err := recipe.BuildPlan(r, nil, nil, none)
	var needs *prompt.NeedsInputError
	if !errors.As(err, &needs) || needs.Needs[0].Name != "name" {
		t.Fatalf("err = %v, want needed input for name", err)
	}
}

func TestPlanRejectsUnknownInput(t *testing.T) {
	r := load(t, recipeYAML)
	if _, err := recipe.BuildPlan(r, map[string]string{"name": "a", "nope": "x"}, nil, none); err == nil {
		t.Fatal("expected an error for an unknown input")
	}
}

func TestPlanReusesExistingObjects(t *testing.T) {
	r := load(t, writeRecipe)
	exists := func(ref object.Ref) bool { return ref.Kind == "Source" }
	plan, err := recipe.BuildPlan(r, map[string]string{"name": "a", "path": "p"}, nil, exists)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].Action != recipe.ActionReuse || plan.Steps[1].Action != recipe.ActionCreate {
		t.Errorf("actions = %s, %s; want reuse, create", plan.Steps[0].Action, plan.Steps[1].Action)
	}
	if !plan.NeedsApproval() {
		t.Error("a plan that creates an object needs approval")
	}
}

func TestPlanRebind(t *testing.T) {
	r := load(t, `
recipe: r
inputs:
  - name: name
steps:
  - object: File/${name}
    use:
      source: Source/${name}
`)
	plan, err := recipe.BuildPlan(r, map[string]string{"name": "a"}, map[string]string{"File/a.source": "Source/other"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Steps[0].Use["source"]; got != "Source/other" {
		t.Errorf("binding = %q, want Source/other", got)
	}
	if _, err := recipe.BuildPlan(r, map[string]string{"name": "a"}, map[string]string{"File/a.nope": "Source/x"}, none); err == nil {
		t.Error("expected an error for a binding that matches no slot")
	}
}

const writeRecipe = `
recipe: write-file
inputs:
  - name: name
  - name: path
steps:
  - object: Source/${name}
    set:
      value: hello
  - object: File/${name}
    set:
      path: ${path}
    use:
      source: Source/${name}
`

func newRunner(t *testing.T) (*recipe.Runner, *object.Store) {
	t.Helper()
	dir := t.TempDir()
	kinds := filepath.Join(dir, "kinds")
	if err := os.Mkdir(kinds, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"source.yaml": sourceKind, "file.yaml": fileKind} {
		if err := os.WriteFile(filepath.Join(kinds, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := kind.LoadRegistry(kinds)
	if err != nil {
		t.Fatal(err)
	}
	store := object.NewStore(filepath.Join(dir, "home"))
	eng := engine.New(engine.Config{Registry: reg, Store: store, Platform: platform.Current()})
	return &recipe.Runner{Engine: eng, Store: store}, store
}

func TestRunNeedsApprovalBeforeCreatingAnything(t *testing.T) {
	runner, store := newRunner(t)
	r := load(t, writeRecipe)
	out := filepath.Join(t.TempDir(), "out.txt")
	req := recipe.Request{Recipe: r, Inputs: map[string]string{"name": "a", "path": out}}

	result, err := runner.Run(context.Background(), req)
	var approval *recipe.ApprovalError
	if !errors.As(err, &approval) {
		t.Fatalf("err = %v, want an approval error", err)
	}
	if len(result.Plan.Steps) != 2 || result.Plan.Steps[1].Use["source"] != "Source/a" {
		t.Errorf("plan = %+v", result.Plan)
	}
	if store.Exists(mustRef(t, "Source/a")) || store.Exists(mustRef(t, "File/a")) {
		t.Fatal("nothing may be created before the user approves")
	}
}

func TestRunCreatesAppliesAndThenReuses(t *testing.T) {
	runner, store := newRunner(t)
	r := load(t, writeRecipe)
	out := filepath.Join(t.TempDir(), "out.txt")
	req := recipe.Request{Recipe: r, Inputs: map[string]string{"name": "a", "path": out}, Approved: true}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reports) != 2 {
		t.Errorf("reports = %d, want 2", len(result.Reports))
	}
	if data, err := os.ReadFile(out); err != nil || string(data) == "" {
		t.Errorf("file not written: %v", err)
	}
	if got := mustLoad(t, store, "File/a").Uses["source"]; got.String() != "Source/a" {
		t.Errorf("binding = %s, want Source/a", got)
	}

	// Everything exists now, so a second run needs no approval.
	req.Approved = false
	result, err = runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, s := range result.Plan.Steps {
		if s.Action != recipe.ActionReuse {
			t.Errorf("%s: action = %s, want reuse", s.Object, s.Action)
		}
	}
}

func mustRef(t *testing.T, s string) object.Ref {
	t.Helper()
	ref, err := object.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustLoad(t *testing.T, store *object.Store, ref string) *object.Object {
	t.Helper()
	o, err := store.Load(mustRef(t, ref))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRunFillsMissingFieldsOfExistingObjects(t *testing.T) {
	runner, store := newRunner(t)
	r := load(t, writeRecipe)
	if err := runner.Engine.Create(mustRef(t, "Source/a"), nil, nil); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.txt")
	req := recipe.Request{Recipe: r, Inputs: map[string]string{"name": "a", "path": out}, Approved: true}
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, store, "Source/a").Set["value"]; got != "hello" {
		t.Errorf("value = %q, want hello", got)
	}

	// A value the user chose is kept.
	if err := runner.Engine.Fill(mustRef(t, "Source/a"), map[string]string{"value": "other"}); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, store, "Source/a").Set["value"]; got != "hello" {
		t.Errorf("value = %q, an existing value must be kept", got)
	}
}
