package engine

import "github.com/aravindakrc/toolbox/internal/prompt"

// FieldReport describes one field of an object.
type FieldReport struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	From    string `json:"from,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// Report is the result of showing, applying or verifying an object.
type Report struct {
	Object   string            `json:"object"`
	Fields   []FieldReport     `json:"fields"`
	Uses     map[string]string `json:"uses,omitempty"`
	Outputs  map[string]string `json:"outputs,omitempty"`
	Needs    []prompt.Need     `json:"needs,omitempty"`
	Actions  []string          `json:"actions,omitempty"`
	Verified *bool             `json:"verified,omitempty"`
	Complete bool              `json:"complete"`
}
