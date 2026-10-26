package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aravindakrc/toolbox/internal/mcpserver"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/recipe"
)

const writeRecipe = `
recipe: write-file
description: Write a file
inputs:
  - name: name
    description: Name of the file
  - name: path
    description: Where the file goes
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

func callRecipe(t *testing.T, s *mcp.ClientSession, args map[string]any) mcpserver.RecipeResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "run_recipe", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("run_recipe: %v %+v", err, res)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out mcpserver.RecipeResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecipeNeedsApprovalThenRuns(t *testing.T) {
	cfg := newConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "write.yaml"), []byte(writeRecipe), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Recipes = func() (*recipe.Registry, error) { return recipe.LoadRegistry(dir) }
	s := connect(t, cfg, nil)

	inputs := map[string]any{"name": "a", "path": filepath.ToSlash(filepath.Join(t.TempDir(), "out.txt"))}
	got := callRecipe(t, s, map[string]any{"recipe": "write-file", "inputs": inputs})
	if got.Status != mcpserver.StatusNeedsInput || got.Plan == nil || len(got.Plan.Steps) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if cfg.Store.Exists(object.Ref{Kind: "Source", Name: "a"}) {
		t.Fatal("nothing may be created before approval")
	}

	got = callRecipe(t, s, map[string]any{"recipe": "write-file", "inputs": inputs, "approved": true})
	if got.Status != mcpserver.StatusOK || len(got.Reports) != 2 {
		t.Fatalf("result = %+v", got)
	}
}

func TestRecipeReportsMissingInputs(t *testing.T) {
	cfg := newConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "write.yaml"), []byte(writeRecipe), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Recipes = func() (*recipe.Registry, error) { return recipe.LoadRegistry(dir) }
	s := connect(t, cfg, nil)

	got := callRecipe(t, s, map[string]any{"recipe": "write-file"})
	if got.Status != mcpserver.StatusNeedsInput || len(got.Needs) != 2 {
		t.Fatalf("result = %+v", got)
	}
}

func TestRecipesAreOfferedAsPrompts(t *testing.T) {
	cfg := newConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "write.yaml"), []byte(writeRecipe), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Recipes = func() (*recipe.Registry, error) { return recipe.LoadRegistry(dir) }
	s := connect(t, cfg, nil)

	list, err := s.ListPrompts(context.Background(), nil)
	if err != nil || len(list.Prompts) != 1 || list.Prompts[0].Name != "tbx-write-file" || len(list.Prompts[0].Arguments) != 2 {
		t.Fatalf("prompts = %+v, err %v", list, err)
	}
	got, err := s.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "tbx-write-file", Arguments: map[string]string{"name": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	text := got.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"write-file", "- name: a", "without approved first", "markdown table"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text misses %q:\n%s", want, text)
		}
	}
}

func TestSlowRecipeAnswersRunningThenResult(t *testing.T) {
	cfg := newConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "write.yaml"), []byte(writeRecipe), 0o644); err != nil {
		t.Fatal(err)
	}
	var slow atomic.Bool
	cfg.Recipes = func() (*recipe.Registry, error) {
		if slow.Load() {
			time.Sleep(400 * time.Millisecond)
		}
		return recipe.LoadRegistry(dir)
	}
	cfg.RecipeWait = 50 * time.Millisecond
	s := connect(t, cfg, nil)

	slow.Store(true)
	inputs := map[string]any{"name": "a", "path": filepath.ToSlash(filepath.Join(t.TempDir(), "out.txt"))}
	args := map[string]any{"recipe": "write-file", "inputs": inputs, "approved": true}
	if got := callRecipe(t, s, args); got.Status != mcpserver.StatusRunning {
		t.Fatalf("first call = %+v", got)
	}
	var got mcpserver.RecipeResult
	for i := 0; i < 40 && got.Status != mcpserver.StatusOK; i++ {
		got = callRecipe(t, s, args)
	}
	if got.Status != mcpserver.StatusOK || len(got.Reports) != 2 {
		t.Fatalf("second call = %+v", got)
	}
}
