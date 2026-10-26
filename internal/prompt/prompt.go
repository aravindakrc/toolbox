// Package prompt defines how tbx asks a person for input and how it reports
// that input or a manual action is still needed.
package prompt

import (
	"context"
	"fmt"
	"strings"
)

// Prompter asks the user for information. A nil Prompter means the session
// is non-interactive.
type Prompter interface {
	// Ask returns free-form text. An empty answer means "no value".
	Ask(ctx context.Context, question string) (string, error)
	// Choose returns one of options.
	Choose(ctx context.Context, question string, options []string) (string, error)
	// Acknowledge shows a message and waits until the user confirms.
	Acknowledge(ctx context.Context, message string) error
}

// NeedType says what kind of input is missing.
type NeedType string

const (
	NeedField NeedType = "field"
	NeedSlot  NeedType = "slot"
)

// Need describes one missing piece of input.
type Need struct {
	Name    string   `json:"name"`
	Type    NeedType `json:"type"`
	Message string   `json:"message"`
	Options []string `json:"options,omitempty"`
}

// NeedsInputError reports input that must be supplied before work can continue.
type NeedsInputError struct {
	Needs []Need
}

func (e *NeedsInputError) Error() string {
	parts := make([]string, len(e.Needs))
	for i, n := range e.Needs {
		parts[i] = fmt.Sprintf("%s %q: %s", n.Type, n.Name, n.Message)
	}
	return "input needed: " + strings.Join(parts, "; ")
}

// NeedsHumanError reports a manual step that cannot be performed in a
// non-interactive session.
type NeedsHumanError struct {
	Message string
}

func (e *NeedsHumanError) Error() string {
	return "manual step required: " + e.Message
}
