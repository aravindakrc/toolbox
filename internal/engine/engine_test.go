package engine_test

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
  destroy: rm -rf "$TBX_PATH"
`

const manualKind = `
kind: Note
fields:
  text:
    type: string
    required: true
    sources:
      - flag: text
provider:
  apply:
    manual: docs/note.md
`

// acknowledger accepts every manual step.
type acknowledger struct{ shown []string }

func (a *acknowledger) Ask(context.Context, string) (string, error) { return "", nil }

func (a *acknowledger) Choose(context.Context, string, []string) (string, error) { return "", nil }

func (a *acknowledger) Acknowledge(_ context.Context, message string) error {
	a.shown = append(a.shown, message)
	return nil
}

func newEngine(t *testing.T, dryRun bool) (*engine.Engine, *object.Store) {
	t.Helper()
	return newEngineWith(t, dryRun, nil)
}

func newEngineWith(t *testing.T, dryRun bool, prompter prompt.Prompter) (*engine.Engine, *object.Store) {
	t.Helper()
	dir := t.TempDir()
	kinds := filepath.Join(dir, "kinds")
	if err := os.Mkdir(kinds, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(kinds, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kinds, "docs", "note.md"), []byte("Write ${text} down."), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"source.yaml": sourceKind, "file.yaml": fileKind, "note.yaml": manualKind} {
		if err := os.WriteFile(filepath.Join(kinds, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := kind.LoadRegistry(kinds)
	if err != nil {
		t.Fatal(err)
	}
	store := object.NewStore(filepath.Join(dir, "home"))
	return engine.New(engine.Config{Registry: reg, Store: store, Platform: platform.Current(), DryRun: dryRun, Prompter: prompter}), store
}

func mustRef(t *testing.T, s string) object.Ref {
	t.Helper()
	ref, err := object.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestApplyIsIdempotent(t *testing.T) {
	eng, _ := newEngine(t, false)
	src, file := mustRef(t, "Source/s"), mustRef(t, "File/f")
	target := filepath.Join(t.TempDir(), "out.txt")

	if err := eng.Create(src, map[string]string{"value": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.Create(file, nil, nil); err != nil {
		t.Fatal(err)
	}
	opts := engine.Options{
		Flags: map[string]string{"path": target},
		Uses:  map[string]object.Ref{"source": src},
	}

	report, err := eng.Apply(context.Background(), file, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Actions; len(got) != 1 || got[0] != "applied" {
		t.Fatalf("first apply actions = %v", got)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	report, err = eng.Apply(context.Background(), file, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Actions; len(got) != 1 || got[0] != "already present" {
		t.Fatalf("second apply actions = %v", got)
	}
	if report.Verified == nil || !*report.Verified {
		t.Fatal("expected verified")
	}
}

func TestNeverAutoBindsSingleCandidate(t *testing.T) {
	eng, _ := newEngine(t, false)
	if err := eng.Create(mustRef(t, "Source/only"), map[string]string{"value": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	file := mustRef(t, "File/f")
	if err := eng.Create(file, nil, nil); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Apply(context.Background(), file, engine.Options{Flags: map[string]string{"path": "unused"}})
	var needs *prompt.NeedsInputError
	if !errors.As(err, &needs) {
		t.Fatalf("err = %v, want NeedsInputError", err)
	}
	if len(needs.Needs) != 1 || needs.Needs[0].Type != prompt.NeedSlot || needs.Needs[0].Options[0] != "Source/only" {
		t.Fatalf("needs = %+v", needs.Needs)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	eng, _ := newEngine(t, true)
	src, file := mustRef(t, "Source/s"), mustRef(t, "File/f")
	target := filepath.Join(t.TempDir(), "out.txt")
	if err := eng.Create(src, map[string]string{"value": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.Create(file, nil, nil); err != nil {
		t.Fatal(err)
	}

	report, err := eng.Apply(context.Background(), file, engine.Options{
		Flags: map[string]string{"path": target},
		Uses:  map[string]object.Ref{"source": src},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Actions; len(got) != 1 || got[0] != "would apply" {
		t.Fatalf("actions = %v", got)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("dry run created the file")
	}
}

func TestDeleteRunsDestroyAndRemovesObject(t *testing.T) {
	eng, store := newEngine(t, false)
	src, file := mustRef(t, "Source/s"), mustRef(t, "File/f")
	target := filepath.Join(t.TempDir(), "out.txt")
	if err := eng.Create(src, map[string]string{"value": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.Create(file, map[string]string{"path": target}, map[string]object.Ref{"source": src}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Apply(context.Background(), file, engine.Options{}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Delete(context.Background(), src); err == nil {
		t.Fatal("deleting a bound object should fail")
	}
	if _, err := eng.Delete(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("destroy did not remove the file")
	}
	if store.Exists(file) {
		t.Fatal("object was not removed")
	}
	if _, err := eng.Delete(context.Background(), src); err != nil {
		t.Fatalf("delete after dependents are gone: %v", err)
	}
}

func TestManualStepShowsFilledInInstructions(t *testing.T) {
	eng, _ := newEngine(t, false)
	ref := mustRef(t, "Note/n")
	if err := eng.Create(ref, nil, nil); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Apply(context.Background(), ref, engine.Options{Flags: map[string]string{"text": "the plan"}})
	var human *prompt.NeedsHumanError
	if !errors.As(err, &human) || human.Message != "Write the plan down." {
		t.Fatalf("err = %v, want a manual step with the filled-in text", err)
	}
}

func TestManualKindIsRememberedOnceApplied(t *testing.T) {
	ack := &acknowledger{}
	eng, _ := newEngineWith(t, false, ack)
	ref := mustRef(t, "Note/n")
	if err := eng.Create(ref, map[string]string{"text": "the plan"}, nil); err != nil {
		t.Fatal(err)
	}

	for i, want := range []string{"applied", "already present"} {
		report, err := eng.Apply(context.Background(), ref, engine.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got := report.Actions; len(got) != 1 || got[0] != want {
			t.Fatalf("apply %d: actions = %v, want %q", i+1, got, want)
		}
	}
	if len(ack.shown) != 1 {
		t.Fatalf("the manual step was shown %d times, want once", len(ack.shown))
	}
}
