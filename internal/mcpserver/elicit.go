package mcpserver

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/prompt"
)

// responses holds what the user answered when the client asked them.
type responses struct {
	fields   map[string]string
	uses     map[string]object.Ref
	declined bool
}

// requestKey names an input request. The type prefix keeps a field and a slot
// with the same name apart.
func requestKey(n prompt.Need) string { return string(n.Type) + ":" + n.Name }

func inputRequests(needs []prompt.Need) mcp.InputRequestMap {
	requests := make(mcp.InputRequestMap, len(needs))
	for _, n := range needs {
		property := map[string]any{"type": "string", "title": n.Name}
		if len(n.Options) > 0 {
			property["enum"] = n.Options
		}
		requests[requestKey(n)] = &mcp.ElicitParams{
			Message: n.Message,
			RequestedSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{n.Name: property},
				"required":   []string{n.Name},
			},
		}
	}
	return requests
}

// collectResponses reads the answers the client sent back for earlier input
// requests. A call without any returns empty responses.
func collectResponses(req *mcp.CallToolRequest) (responses, error) {
	out := responses{fields: map[string]string{}, uses: map[string]object.Ref{}}
	if req == nil || req.Params == nil {
		return out, nil
	}
	for key, raw := range req.Params.InputResponses {
		result, ok := raw.(*mcp.ElicitResult)
		if !ok {
			continue
		}
		if result.Action != "accept" {
			out.declined = true
			continue
		}
		kindOfNeed, name, _ := strings.Cut(key, ":")
		value, _ := result.Content[name].(string)
		switch prompt.NeedType(kindOfNeed) {
		case prompt.NeedSlot:
			ref, err := object.ParseRef(value)
			if err != nil {
				return out, fmt.Errorf("answer for slot %q: %w", name, err)
			}
			out.uses[name] = ref
		case prompt.NeedField:
			out.fields[name] = value
		}
	}
	return out, nil
}
