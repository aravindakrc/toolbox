package kind

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads and validates a single kind file.
func Load(path string) (*Kind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var k Kind
	if err := dec.Decode(&k); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	k.Dir = abs
	if err := k.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &k, nil
}

// Registry holds the available kinds by name.
type Registry struct {
	kinds map[string]*Kind
}

// NewRegistry builds a registry, rejecting duplicate names and slots that
// point at unknown kinds.
func NewRegistry(kinds ...*Kind) (*Registry, error) {
	r := &Registry{kinds: make(map[string]*Kind, len(kinds))}
	for _, k := range kinds {
		if existing, dup := r.kinds[k.Name]; dup {
			return nil, fmt.Errorf("kind %s is defined twice (%s and %s)", k.Name, existing.Dir, k.Dir)
		}
		r.kinds[k.Name] = k
	}
	var errs []error
	for _, name := range r.Names() {
		k := r.kinds[name]
		for _, slot := range k.SortedSlots() {
			if _, ok := r.kinds[k.Uses[slot]]; !ok {
				errs = append(errs, fmt.Errorf("kind %s: slot %q uses unknown kind %q", name, slot, k.Uses[slot]))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return r, nil
}

// LoadRegistry loads every *.yaml and *.yml file in the given directories.
// Directories that do not exist are skipped.
func LoadRegistry(dirs ...string) (*Registry, error) {
	var kinds []*Kind
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
				continue
			}
			k, err := Load(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			kinds = append(kinds, k)
		}
	}
	return NewRegistry(kinds...)
}

// Get returns the kind with the given name.
func (r *Registry) Get(name string) (*Kind, bool) {
	k, ok := r.kinds[name]
	return k, ok
}

// Names returns all kind names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
