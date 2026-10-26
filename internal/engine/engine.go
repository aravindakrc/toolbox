// Package engine completes objects: it binds slots, resolves fields, runs
// providers and records the result.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/aravindakrc/toolbox/internal/kind"
	"github.com/aravindakrc/toolbox/internal/object"
	"github.com/aravindakrc/toolbox/internal/platform"
	"github.com/aravindakrc/toolbox/internal/prompt"
	"github.com/aravindakrc/toolbox/internal/provider"
	"github.com/aravindakrc/toolbox/internal/resolver"
)

// Config holds the engine's dependencies and settings.
type Config struct {
	Registry *kind.Registry
	Store    *object.Store
	// Prompter is used to ask the user. Nil makes the engine non-interactive.
	Prompter prompt.Prompter
	Platform platform.Info
	// DryRun makes Apply run discovery only and write nothing.
	DryRun bool
	// Stdout and Stderr receive provider output.
	Stdout io.Writer
	Stderr io.Writer
	// Log receives progress messages.
	Log io.Writer
}

// Engine operates on objects.
type Engine struct {
	cfg      Config
	runner   *provider.Runner
	resolver *resolver.Resolver
}

// New creates an engine.
func New(cfg Config) *Engine {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	runner := &provider.Runner{
		Platform: cfg.Platform,
		Stdout:   cfg.Stdout,
		Stderr:   cfg.Stderr,
		Prompter: cfg.Prompter,
	}
	return &Engine{
		cfg:    cfg,
		runner: runner,
		resolver: &resolver.Resolver{
			Platform:   cfg.Platform,
			DataDir:    cfg.Store.DataDir,
			LookupEnv:  os.LookupEnv,
			RunCommand: runner.Output,
			Prompter:   cfg.Prompter,
		},
	}
}

// Options are per-run inputs for the object being operated on.
type Options struct {
	// Flags fill fields that declare a flag source.
	Flags map[string]string
	// Answers are field values the user supplied when asked. They are stored
	// on the object, like answers given at a prompt.
	Answers map[string]string
	// Uses binds slots to objects, overriding the object's own bindings.
	Uses map[string]object.Ref
}

// Create stores a new object. set fills fields and uses binds slots.
func (e *Engine) Create(ref object.Ref, set map[string]string, uses map[string]object.Ref) error {
	k, err := e.kind(ref)
	if err != nil {
		return err
	}
	if e.cfg.Store.Exists(ref) {
		return fmt.Errorf("%s already exists", ref)
	}
	for name := range set {
		if _, ok := k.Fields[name]; !ok {
			return fmt.Errorf("kind %s has no field %q", k.Name, name)
		}
	}
	for slot, target := range uses {
		if err := checkBinding(k, slot, target); err != nil {
			return err
		}
	}
	return e.cfg.Store.Save(&object.Object{Ref: ref, Uses: uses, Set: set})
}

// Fill sets the given fields on an existing object that has no value for them
// yet. Values the user already chose are kept.
func (e *Engine) Fill(ref object.Ref, set map[string]string) error {
	k, err := e.kind(ref)
	if err != nil {
		return err
	}
	obj, err := e.cfg.Store.Load(ref)
	if err != nil {
		return err
	}
	changed := false
	for name, value := range set {
		if _, ok := k.Fields[name]; !ok {
			return fmt.Errorf("kind %s has no field %q", k.Name, name)
		}
		if _, have := obj.Set[name]; have || value == "" {
			continue
		}
		if obj.Set == nil {
			obj.Set = map[string]string{}
		}
		obj.Set[name] = value
		changed = true
	}
	if !changed {
		return nil
	}
	return e.cfg.Store.Save(obj)
}

func (e *Engine) kind(ref object.Ref) (*kind.Kind, error) {
	k, ok := e.cfg.Registry.Get(ref.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", ref.Kind)
	}
	return k, nil
}

func checkBinding(k *kind.Kind, slot string, target object.Ref) error {
	want, ok := k.Uses[slot]
	if !ok {
		return fmt.Errorf("kind %s has no slot %q", k.Name, slot)
	}
	if target.Kind != want {
		return fmt.Errorf("slot %q of %s needs a %s, got %s", slot, k.Name, want, target)
	}
	return nil
}

// prepared is an object with its slots bound and its fields resolved.
type prepared struct {
	kind    *kind.Kind
	object  *object.Object
	result  resolver.Result
	slots   map[string]map[string]string
	changed bool
}

// env builds the script environment, including the object's data directory.
func (e *Engine) env(p *prepared) map[string]string {
	env := provider.Env(p.fieldValues(), p.slots)
	env["TBX_DATA_DIR"] = e.cfg.Store.DataDir(p.object.Ref)
	return env
}

func (p *prepared) fieldValues() map[string]string {
	values := make(map[string]string, len(p.result.Fields))
	for name, r := range p.result.Fields {
		values[name] = r.Value
	}
	return values
}

// bind makes sure every slot of the object points at an object. It never
// picks one on the user's behalf: unbound slots are asked about, or reported
// as needed input.
func (e *Engine) bind(ctx context.Context, k *kind.Kind, obj *object.Object, opts Options, prompter prompt.Prompter) (changed bool, needs []prompt.Need, err error) {
	for slot, target := range opts.Uses {
		if err := checkBinding(k, slot, target); err != nil {
			return false, nil, err
		}
		if obj.Uses[slot] != target {
			setBinding(obj, slot, target)
			changed = true
		}
	}

	for _, slot := range k.SortedSlots() {
		if current, bound := obj.Uses[slot]; bound {
			if err := checkBinding(k, slot, current); err != nil {
				return false, nil, err
			}
			if !e.cfg.Store.Exists(current) {
				return false, nil, fmt.Errorf("slot %q of %s is bound to %s, which does not exist", slot, obj.Ref, current)
			}
			continue
		}

		candidates, err := e.cfg.Store.List(k.Uses[slot])
		if err != nil {
			return false, nil, err
		}
		options := make([]string, len(candidates))
		for i, c := range candidates {
			options[i] = c.String()
		}
		need := prompt.Need{
			Name:    slot,
			Type:    prompt.NeedSlot,
			Message: fmt.Sprintf("choose the %s object to use for %s", k.Uses[slot], slot),
			Options: options,
		}
		if prompter == nil || len(options) == 0 {
			if len(options) == 0 {
				need.Message = fmt.Sprintf("no %s objects exist; create one with `tbx new`", k.Uses[slot])
			}
			needs = append(needs, need)
			continue
		}

		choice, err := prompter.Choose(ctx, need.Message, options)
		if err != nil {
			return false, nil, err
		}
		chosen, err := object.ParseRef(choice)
		if err != nil || !slices.Contains(options, choice) {
			return false, nil, fmt.Errorf("%q is not one of the offered objects", choice)
		}
		setBinding(obj, slot, chosen)
		changed = true
	}
	return changed, needs, nil
}

func setBinding(obj *object.Object, slot string, target object.Ref) {
	if obj.Uses == nil {
		obj.Uses = map[string]object.Ref{}
	}
	obj.Uses[slot] = target
}

// resolve binds slots and resolves fields. completeDep supplies the values of
// each bound object.
func (e *Engine) resolve(ctx context.Context, ref object.Ref, opts Options, interactive bool, dep func(object.Ref) (map[string]string, error)) (*prepared, error) {
	k, err := e.kind(ref)
	if err != nil {
		return nil, err
	}
	obj, err := e.cfg.Store.Load(ref)
	if err != nil {
		return nil, err
	}

	prompter := e.cfg.Prompter
	if !interactive {
		prompter = nil
	}
	p := &prepared{kind: k, object: obj, slots: map[string]map[string]string{}}

	for name, value := range opts.Answers {
		if _, ok := k.Fields[name]; !ok {
			return nil, fmt.Errorf("kind %s has no field %q", k.Name, name)
		}
		if obj.Set[name] != value {
			if obj.Set == nil {
				obj.Set = map[string]string{}
			}
			obj.Set[name] = value
			p.changed = true
		}
	}

	changed, needs, err := e.bind(ctx, k, obj, opts, prompter)
	if err != nil {
		return nil, err
	}
	p.changed = p.changed || changed
	if len(needs) > 0 {
		return p, &prompt.NeedsInputError{Needs: needs}
	}

	for _, slot := range k.SortedSlots() {
		values, err := dep(obj.Uses[slot])
		if err != nil {
			return nil, err
		}
		p.slots[slot] = values
	}

	r := *e.resolver
	r.Prompter = prompter
	p.result, err = r.Resolve(ctx, resolver.Input{Kind: k, Object: obj, Flags: opts.Flags, Slots: p.slots})
	if err != nil {
		return nil, err
	}
	for name, v := range p.result.Answers {
		if obj.Set == nil {
			obj.Set = map[string]string{}
		}
		obj.Set[name] = v
		p.changed = true
	}
	return p, nil
}

// storedValues returns the last recorded values of an object, or none if it
// has not been applied yet.
func (e *Engine) storedValues(ref object.Ref) (map[string]string, error) {
	st, err := e.cfg.Store.LoadStatus(ref)
	if errors.Is(err, object.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return st.Values(), nil
}

func (e *Engine) logf(format string, args ...any) {
	fmt.Fprintf(e.cfg.Log, format+"\n", args...)
}

func (e *Engine) now() time.Time { return time.Now().UTC() }
