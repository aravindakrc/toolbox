// Package kind defines the types of objects tbx manages and loads them from YAML.
package kind

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aravindakrc/toolbox/internal/platform"
)

// FieldType is the type of value a field holds.
type FieldType string

const (
	TypeString  FieldType = "string"
	TypeInteger FieldType = "integer"
	TypeBoolean FieldType = "boolean"
	TypeEnum    FieldType = "enum"
)

// Kind describes one type of object: its fields, the other kinds it uses,
// and the provider that manages it.
type Kind struct {
	Name     string            `yaml:"kind"`
	Uses     map[string]string `yaml:"uses"`
	Fields   map[string]Field  `yaml:"fields"`
	Provider Provider          `yaml:"provider"`

	// Dir is the directory the kind was loaded from. Relative paths used by
	// the provider are resolved against it.
	Dir string `yaml:"-"`
}

// Field is a typed value on an object, filled from its sources in order.
type Field struct {
	Type     FieldType `yaml:"type"`
	Required bool      `yaml:"required"`
	Values   []string  `yaml:"values"`
	Sources  []Source  `yaml:"sources"`
}

// HasFlag reports whether the field has a flag source named flag.
func (f Field) HasFlag(fieldName, flag string) bool {
	for _, s := range f.Sources {
		if s.Type == SourceFlag && s.FlagName(fieldName) == flag {
			return true
		}
	}
	return false
}

// Condition restricts a source or provider step to some platforms.
// An empty field matches everything.
type Condition struct {
	OS   string `yaml:"os"`
	Arch string `yaml:"arch"`
}

// Matches reports whether p satisfies the condition. A nil condition always matches.
func (c *Condition) Matches(p platform.Info) bool {
	if c == nil {
		return true
	}
	return (c.OS == "" || c.OS == p.OS) && (c.Arch == "" || c.Arch == p.Arch)
}

// SourceType names where a field value can come from.
type SourceType string

const (
	SourceFlag    SourceType = "flag"
	SourceEnv     SourceType = "env"
	SourceCommand SourceType = "command"
	SourceRef     SourceType = "ref"
	SourcePrompt  SourceType = "prompt"
	SourceDefault SourceType = "default"
)

var sourceTypes = map[SourceType]bool{
	SourceFlag: true, SourceEnv: true, SourceCommand: true,
	SourceRef: true, SourcePrompt: true, SourceDefault: true,
}

// Source is one way to obtain a field value. Its meaning depends on Type:
// the flag name, environment variable, command, "slot.field" reference,
// prompt message or literal default.
type Source struct {
	Type  SourceType
	Value string
	When  *Condition
}

// FlagName returns the flag a flag source reads, defaulting to the field name.
func (s Source) FlagName(fieldName string) string {
	if s.Value != "" {
		return s.Value
	}
	return fieldName
}

// RefParts splits a ref source into its slot and field names.
func (s Source) RefParts() (slot, field string, ok bool) {
	return strings.Cut(s.Value, ".")
}

// UnmarshalYAML decodes a mapping with exactly one source key and an optional "when".
func (s *Source) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a source must be a mapping such as `env: NAME`", node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		if key.Value == "when" {
			var c Condition
			if err := val.Decode(&c); err != nil {
				return err
			}
			s.When = &c
			continue
		}
		t := SourceType(key.Value)
		if !sourceTypes[t] {
			return fmt.Errorf("line %d: unknown source %q", key.Line, key.Value)
		}
		if s.Type != "" {
			return fmt.Errorf("line %d: a source takes exactly one of flag, env, command, ref, prompt, default", key.Line)
		}
		s.Type, s.Value = t, val.Value
	}
	if s.Type == "" {
		return fmt.Errorf("line %d: source has no type", node.Line)
	}
	return nil
}

// Shell selects the interpreter that runs a provider script.
const (
	ShellPortable   = ""
	ShellBash       = "bash"
	ShellPowerShell = "powershell"
	ShellCmd        = "cmd"
)

// Step is one way to carry out a provider operation. Exactly one of Run,
// File or Manual is set.
type Step struct {
	Run    string     `yaml:"run"`
	File   string     `yaml:"file"`
	Manual string     `yaml:"manual"`
	Shell  string     `yaml:"shell"`
	When   *Condition `yaml:"when"`
}

// Operation is the ordered list of alternative steps for one provider
// operation; the first step whose condition matches is used.
type Operation []Step

// UnmarshalYAML accepts a script string, a single step, or a list of steps.
func (o *Operation) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*o = Operation{{Run: node.Value}}
	case yaml.MappingNode:
		var s Step
		if err := node.Decode(&s); err != nil {
			return err
		}
		*o = Operation{s}
	case yaml.SequenceNode:
		var steps []Step
		if err := node.Decode(&steps); err != nil {
			return err
		}
		*o = steps
	default:
		return fmt.Errorf("line %d: an operation must be a script, a step or a list of steps", node.Line)
	}
	return nil
}

// Provider holds the operations that manage an object.
type Provider struct {
	Discover Operation `yaml:"discover"`
	Apply    Operation `yaml:"apply"`
	Verify   Operation `yaml:"verify"`
	Destroy  Operation `yaml:"destroy"`
}

// Defined reports whether the provider has any operation.
func (p Provider) Defined() bool {
	return len(p.Discover) > 0 || len(p.Apply) > 0 || len(p.Verify) > 0 || len(p.Destroy) > 0
}

var (
	kindName  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	identName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

	knownOS   = map[string]bool{"windows": true, "darwin": true, "linux": true}
	knownArch = map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true}
)

// reservedFields cannot be used as field names because the provider
// environment already uses them.
var reservedFields = map[string]bool{"out": true}

// Validate checks the kind for structural errors.
func (k *Kind) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if !kindName.MatchString(k.Name) {
		add("kind name %q must start with an uppercase letter and contain only letters and digits", k.Name)
	}
	for slot, target := range k.Uses {
		if !identName.MatchString(slot) {
			add("slot %q must start with a lowercase letter and contain only letters and digits", slot)
		}
		if !kindName.MatchString(target) {
			add("slot %q uses invalid kind %q", slot, target)
		}
	}
	for _, name := range sortedKeys(k.Fields) {
		k.validateField(name, k.Fields[name], add)
	}
	k.validateOperation("discover", k.Provider.Discover, add)
	k.validateOperation("apply", k.Provider.Apply, add)
	k.validateOperation("verify", k.Provider.Verify, add)
	k.validateOperation("destroy", k.Provider.Destroy, add)

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("kind %s: %w", k.Name, err)
	}
	return nil
}

func (k *Kind) validateField(name string, f Field, add func(string, ...any)) {
	if !identName.MatchString(name) {
		add("field %q must start with a lowercase letter and contain only letters and digits", name)
	}
	if reservedFields[name] {
		add("field name %q is reserved", name)
	}
	switch f.Type {
	case TypeString, TypeInteger, TypeBoolean:
	case TypeEnum:
		if len(f.Values) == 0 {
			add("field %q is an enum and needs values", name)
		}
	default:
		add("field %q has unknown type %q", name, f.Type)
	}
	for _, s := range f.Sources {
		if err := checkCondition(s.When); err != nil {
			add("field %q: %v", name, err)
		}
		if s.Type != SourceRef {
			continue
		}
		slot, field, ok := s.RefParts()
		if !ok || slot == "" || field == "" {
			add("field %q: ref %q must look like slot.field", name, s.Value)
		} else if _, used := k.Uses[slot]; !used {
			add("field %q: ref %q uses undeclared slot %q", name, s.Value, slot)
		}
	}
}

func (k *Kind) validateOperation(name string, op Operation, add func(string, ...any)) {
	for i, s := range op {
		set := 0
		for _, v := range []string{s.Run, s.File, s.Manual} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			add("%s step %d needs exactly one of run, file or manual", name, i+1)
		}
		switch s.Shell {
		case ShellPortable, ShellBash, ShellPowerShell, ShellCmd:
		default:
			add("%s step %d has unknown shell %q", name, i+1, s.Shell)
		}
		if err := checkCondition(s.When); err != nil {
			add("%s step %d: %v", name, i+1, err)
		}
	}
}

func checkCondition(c *Condition) error {
	if c == nil {
		return nil
	}
	if c.OS != "" && !knownOS[c.OS] {
		return fmt.Errorf("unknown os %q in when", c.OS)
	}
	if c.Arch != "" && !knownArch[c.Arch] {
		return fmt.Errorf("unknown arch %q in when", c.Arch)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SortedFields returns the field names in a stable order.
func (k *Kind) SortedFields() []string { return sortedKeys(k.Fields) }

// SortedSlots returns the slot names in a stable order.
func (k *Kind) SortedSlots() []string { return sortedKeys(k.Uses) }
