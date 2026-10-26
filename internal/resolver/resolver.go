// Package resolver fills an object's fields from their declared sources.
package resolver

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
)

// Resolver turns field sources into values.
type Resolver struct {
	Platform platform.Info
	// DataDir returns the data directory of an object.
	DataDir   func(ref object.Ref) string
	LookupEnv func(name string) (string, bool)
	// RunCommand runs a command source and returns its standard output.
	RunCommand func(ctx context.Context, script string) (string, error)
	// Prompter is used for prompt sources. When nil, prompts are skipped and
	// reported as missing input instead.
	Prompter prompt.Prompter
}

// Input is everything needed to resolve one object.
type Input struct {
	Kind   *kind.Kind
	Object *object.Object
	// Flags are per-run values for fields that have a flag source.
	Flags map[string]string
	// Slots holds the values of the object bound to each slot.
	Slots map[string]map[string]string
}

// Result is the outcome of resolving an object.
type Result struct {
	// Fields are the fields that received a value.
	Fields map[string]object.Resolved
	// Missing lists required fields that could not be filled.
	Missing []prompt.Need
	// Answers are values the user typed at a prompt, to be saved on the object.
	Answers map[string]string
}

var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Resolve fills every field of the object. Sources are tried in order and the
// first that yields a value wins; values stored on the object always win.
func (r *Resolver) Resolve(ctx context.Context, in Input) (Result, error) {
	s := &session{
		r:       r,
		ctx:     ctx,
		in:      in,
		fields:  make(map[string]object.Resolved),
		states:  make(map[string]fieldState),
		needs:   make(map[string]prompt.Need),
		answers: make(map[string]string),
	}
	for _, name := range in.Kind.SortedFields() {
		if _, _, err := s.field(name); err != nil {
			return Result{}, err
		}
	}
	for name := range in.Object.Set {
		if _, ok := in.Kind.Fields[name]; !ok {
			return Result{}, fmt.Errorf("%s: field %q is not defined by kind %s", in.Object.Ref, name, in.Kind.Name)
		}
	}

	res := Result{Fields: s.fields, Answers: s.answers}
	for _, name := range in.Kind.SortedFields() {
		if need, ok := s.needs[name]; ok {
			res.Missing = append(res.Missing, need)
		}
	}
	return res, nil
}

type fieldState int

const (
	stateResolving fieldState = iota + 1
	stateDone
)

type session struct {
	r       *Resolver
	ctx     context.Context
	in      Input
	fields  map[string]object.Resolved
	states  map[string]fieldState
	needs   map[string]prompt.Need
	answers map[string]string
}

// field resolves one field on demand, so a default can refer to another field.
func (s *session) field(name string) (object.Resolved, bool, error) {
	switch s.states[name] {
	case stateResolving:
		return object.Resolved{}, false, fmt.Errorf("field %q depends on itself", name)
	case stateDone:
		v, ok := s.fields[name]
		return v, ok, nil
	}
	s.states[name] = stateResolving
	defer func() { s.states[name] = stateDone }()

	def := s.in.Kind.Fields[name]
	v, ok, err := s.lookup(name, def)
	if err != nil {
		return object.Resolved{}, false, err
	}
	if !ok {
		return object.Resolved{}, false, nil
	}
	if err := validate(def, v.Value); err != nil {
		return object.Resolved{}, false, fmt.Errorf("field %q: %w", name, err)
	}
	s.fields[name] = v
	return v, true, nil
}

func (s *session) lookup(name string, def kind.Field) (object.Resolved, bool, error) {
	if v, ok := s.in.Object.Set[name]; ok {
		return object.Resolved{Value: v, From: "set"}, true, nil
	}

	askMessage := ""
	for _, src := range def.Sources {
		if !src.When.Matches(s.r.Platform) {
			continue
		}
		if src.Type == kind.SourcePrompt {
			v, asked, err := s.prompt(name, src)
			if err != nil {
				return object.Resolved{}, false, err
			}
			if v != "" {
				s.answers[name] = v
				return object.Resolved{Value: v, From: "prompt"}, true, nil
			}
			if !asked {
				askMessage = promptMessage(name, src)
			}
			continue
		}
		v, ok, err := s.evaluate(name, src)
		if err != nil {
			return object.Resolved{}, false, err
		}
		if ok {
			return v, true, nil
		}
	}

	if def.Required {
		if askMessage == "" {
			askMessage = fmt.Sprintf("no value available for %s", name)
		}
		s.needs[name] = prompt.Need{Name: name, Type: prompt.NeedField, Message: askMessage}
	}
	return object.Resolved{}, false, nil
}

func (s *session) evaluate(name string, src kind.Source) (object.Resolved, bool, error) {
	switch src.Type {
	case kind.SourceFlag:
		v, ok := s.in.Flags[src.FlagName(name)]
		return object.Resolved{Value: v, From: "flag"}, ok && v != "", nil

	case kind.SourceEnv:
		v, ok := s.r.LookupEnv(src.Value)
		return object.Resolved{Value: v, From: "env " + src.Value}, ok && v != "", nil

	case kind.SourceCommand:
		out, err := s.r.RunCommand(s.ctx, src.Value)
		out = strings.TrimSpace(out)
		// A failing command is an expected way for a source to have no value.
		return object.Resolved{Value: out, From: "command"}, err == nil && out != "", nil

	case kind.SourceRef:
		slot, field, _ := src.RefParts()
		v, ok := s.in.Slots[slot][field]
		return object.Resolved{Value: v, From: "ref " + src.Value}, ok && v != "", nil

	case kind.SourceDefault:
		v, ok, err := s.expand(src.Value)
		if err != nil {
			return object.Resolved{}, false, fmt.Errorf("field %q: %w", name, err)
		}
		return object.Resolved{Value: v, From: "default"}, ok && v != "", nil
	}
	return object.Resolved{}, false, fmt.Errorf("field %q: unsupported source %q", name, src.Type)
}

// prompt asks the user for a value. asked is false when no prompter is available.
func (s *session) prompt(name string, src kind.Source) (value string, asked bool, err error) {
	if s.r.Prompter == nil {
		return "", false, nil
	}
	v, err := s.r.Prompter.Ask(s.ctx, promptMessage(name, src))
	if err != nil {
		return "", true, err
	}
	return strings.TrimSpace(v), true, nil
}

func promptMessage(name string, src kind.Source) string {
	if src.Value != "" {
		return src.Value
	}
	return "Value for " + name
}

// expand substitutes ${name} with another field's value or a built-in:
// home, data (the object's data directory), os, arch, and the object's name. A field wins over a built-in.
// ok is false when a referenced field has no value.
func (s *session) expand(text string) (out string, ok bool, err error) {
	ok = true
	out = variable.ReplaceAllStringFunc(text, func(m string) string {
		if err != nil {
			return m
		}
		var v string
		var found bool
		v, found, err = s.variableValue(m[2 : len(m)-1])
		if !found {
			ok = false
		}
		return v
	})
	return out, ok && err == nil, err
}

func (s *session) variableValue(name string) (string, bool, error) {
	if _, isField := s.in.Kind.Fields[name]; isField {
		v, ok, err := s.field(name)
		return v.Value, ok, err
	}
	switch name {
	case "home":
		home, err := os.UserHomeDir()
		return home, err == nil, err
	case "data":
		if s.r.DataDir == nil {
			return "", false, nil
		}
		return s.r.DataDir(s.in.Object.Ref), true, nil
	case "os":
		return s.r.Platform.OS, true, nil
	case "arch":
		return s.r.Platform.Arch, true, nil
	case "name":
		return s.in.Object.Ref.Name, true, nil
	}
	return "", false, fmt.Errorf("unknown variable ${%s}", name)
}

func validate(def kind.Field, value string) error {
	switch def.Type {
	case kind.TypeInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
	case kind.TypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%q is not a boolean", value)
		}
	case kind.TypeEnum:
		for _, allowed := range def.Values {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of %s", value, strings.Join(def.Values, ", "))
	}
	return nil
}
