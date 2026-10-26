package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/mcpserver"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
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
      - prompt: Where should the file go?
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

func newConfig(t *testing.T) mcpserver.Config {
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
	return mcpserver.Config{
		Kinds:    func() (*kind.Registry, error) { return kind.LoadRegistry(kinds) },
		Store:    object.NewStore(filepath.Join(dir, "home")),
		Platform: platform.Current(),
	}
}

func connect(t *testing.T, cfg mcpserver.Config, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(cfg).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) mcpserver.Result {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %+v", tool, res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out mcpserver.Result
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func setup(t *testing.T, s *mcp.ClientSession) {
	t.Helper()
	for _, args := range []map[string]any{
		{"object": "Source/s", "set": map[string]any{"value": "hello"}},
		{"object": "File/f"},
	} {
		if got := call(t, s, "create_object", args); got.Status != mcpserver.StatusOK {
			t.Fatalf("create: %+v", got)
		}
	}
}

func TestApplyReportsNeedsAndNeverBinds(t *testing.T) {
	s := connect(t, newConfig(t), nil)
	setup(t, s)

	got := call(t, s, "apply_object", map[string]any{"object": "File/f"})
	if got.Status != mcpserver.StatusNeedsInput || len(got.Needs) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if need := got.Needs[0]; need.Name != "source" || len(need.Options) != 1 || need.Options[0] != "Source/s" {
		t.Fatalf("need = %+v", need)
	}

	target := filepath.ToSlash(filepath.Join(t.TempDir(), "out.txt"))
	got = call(t, s, "apply_object", map[string]any{
		"object":  "File/f",
		"use":     map[string]any{"source": "Source/s"},
		"answers": map[string]any{"path": target},
	})
	if got.Status != mcpserver.StatusOK {
		t.Fatalf("result = %+v", got)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestElicitationCollectsAnswersFromClient(t *testing.T) {
	cfg := newConfig(t)
	cfg.Elicit = true
	target := filepath.ToSlash(filepath.Join(t.TempDir(), "out.txt"))

	var asked []string
	s := connect(t, cfg, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked = append(asked, r.Params.Message)
			props := r.Params.RequestedSchema.(map[string]any)["properties"].(map[string]any)
			if _, ok := props["source"]; ok {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"source": "Source/s"}}, nil
			}
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"path": target}}, nil
		},
	})
	setup(t, s)

	got := call(t, s, "apply_object", map[string]any{"object": "File/f"})
	if got.Status != mcpserver.StatusOK {
		t.Fatalf("result = %+v (asked %v)", got, asked)
	}
	if len(asked) == 0 {
		t.Fatal("the user was never asked")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	s := connect(t, newConfig(t), nil)
	call(t, s, "create_object", map[string]any{"object": "Source/s", "set": map[string]any{"value": "x"}})
	call(t, s, "create_object", map[string]any{
		"object": "File/f",
		"set":    map[string]any{"path": filepath.ToSlash(filepath.Join(t.TempDir(), "out.txt"))},
		"use":    map[string]any{"source": "Source/s"},
	})

	if got := call(t, s, "delete_object", map[string]any{"object": "File/f"}); got.Status != mcpserver.StatusNeedsInput {
		t.Fatalf("unconfirmed delete = %+v", got)
	}
	if got := call(t, s, "delete_object", map[string]any{"object": "File/f", "confirm": true}); got.Status != mcpserver.StatusOK {
		t.Fatalf("confirmed delete = %+v", got)
	}
}

func TestReadOnlyHidesMutatingTools(t *testing.T) {
	cfg := newConfig(t)
	cfg.ReadOnly = true
	s := connect(t, cfg, nil)

	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, hidden := range []string{"create_object", "apply_object", "delete_object"} {
		if names[hidden] {
			t.Errorf("%s should be hidden in read-only mode", hidden)
		}
	}
	if !names["show_object"] || !names["list_kinds"] {
		t.Errorf("read tools missing: %v", names)
	}
}
