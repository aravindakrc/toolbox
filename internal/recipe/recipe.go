// Package recipe describes a repeatable setup as a list of objects to create
// and apply, so a whole task needs one request instead of a long prompt.
package recipe

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

// Recipe is a named, ordered list of objects that together complete a task.
type Recipe struct {
	Name        string  `yaml:"recipe"`
	Description string  `yaml:"description"`
	Inputs      []Input `yaml:"inputs"`
	Steps       []Step  `yaml:"steps"`
}

// Input is a value the user supplies when running a recipe. Defaults and
// steps may refer to inputs as ${name}.
type Input struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
	Optional    bool   `yaml:"optional"`
}

// Required reports whether the input must be supplied.
func (i Input) Required() bool { return i.Default == "" && !i.Optional }

// Step is one object of a recipe. Set fills fields and Use binds slots; both
// are proposals that the user approves before anything is created.
type Step struct {
	Object string            `yaml:"object"`
	Set    map[string]string `yaml:"set"`
	Use    map[string]string `yaml:"use"`
}

// Load reads and validates a recipe file.
func Load(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var r Recipe
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := r.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

func (r *Recipe) validate() error {
	if r.Name == "" {
		return errors.New("recipe name is missing")
	}
	if len(r.Steps) == 0 {
		return fmt.Errorf("recipe %s has no steps", r.Name)
	}
	seen := map[string]bool{}
	for _, in := range r.Inputs {
		if in.Name == "" || seen[in.Name] {
			return fmt.Errorf("recipe %s: input %q is missing or repeated", r.Name, in.Name)
		}
		seen[in.Name] = true
	}
	for i, s := range r.Steps {
		if s.Object == "" {
			return fmt.Errorf("recipe %s: step %d has no object", r.Name, i+1)
		}
	}
	return nil
}

// Registry holds the loaded recipes by name.
type Registry struct {
	recipes map[string]*Recipe
}

// LoadRegistry loads every *.yaml and *.yml file in the given directories.
// Directories that do not exist are skipped.
func LoadRegistry(dirs ...string) (*Registry, error) {
	reg := &Registry{recipes: map[string]*Recipe{}}
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
			r, err := Load(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if _, dup := reg.recipes[r.Name]; dup {
				return nil, fmt.Errorf("recipe %s is defined twice", r.Name)
			}
			reg.recipes[r.Name] = r
		}
	}
	return reg, nil
}

// Get returns the recipe with the given name.
func (r *Registry) Get(name string) (*Recipe, bool) {
	rec, ok := r.recipes[name]
	return rec, ok
}

// Names returns all recipe names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.recipes))
	for n := range r.recipes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
