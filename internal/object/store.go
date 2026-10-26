package object

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ErrNotFound is returned when an object or status file does not exist.
var ErrNotFound = errors.New("not found")

// Store keeps each object in its own directory under a root directory:
// objects/<Kind>/<name>/ holds object.yaml (declared), state.yaml (observed)
// and data/ for the files the object produces.
type Store struct {
	root string
}

const (
	objectFile = "object.yaml"
	stateFile  = "state.yaml"
)

// NewStore returns a store rooted at dir. The directory is created on first write.
func NewStore(dir string) *Store {
	return &Store{root: dir}
}

func (s *Store) objectDir(ref Ref) string {
	return filepath.Join(s.root, "objects", ref.Kind, ref.Name)
}

// Exists reports whether the declared object exists.
func (s *Store) Exists(ref Ref) bool {
	_, err := os.Stat(filepath.Join(s.objectDir(ref), objectFile))
	return err == nil
}

// Load reads a declared object.
func (s *Store) Load(ref Ref) (*Object, error) {
	var o Object
	if err := s.read(ref, objectFile, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// Save writes a declared object.
func (s *Store) Save(o *Object) error {
	return s.write(o.Ref, objectFile, o)
}

// LoadStatus reads an object's observed status.
func (s *Store) LoadStatus(ref Ref) (*Status, error) {
	var st Status
	if err := s.read(ref, stateFile, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// SaveStatus writes an object's observed status.
func (s *Store) SaveStatus(st *Status) error {
	return s.write(st.Ref, stateFile, st)
}

// DataDir is the directory where an object keeps the files it produces.
func (s *Store) DataDir(ref Ref) string {
	return filepath.Join(s.objectDir(ref), "data")
}

// Delete removes an object with its status and data. A missing object is not an error.
func (s *Store) Delete(ref Ref) error {
	dir := s.objectDir(ref)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// Drop the kind directory once its last object is gone; fails harmlessly while it is not empty.
	_ = os.Remove(filepath.Dir(dir))
	return nil
}

// List returns the objects of a kind in sorted order.
func (s *Store) List(kind string) ([]Ref, error) {
	if !kindPart.MatchString(kind) {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "objects", kind))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, e := range entries {
		ref := Ref{Kind: kind, Name: e.Name()}
		if !e.IsDir() || !namePart.MatchString(ref.Name) || !s.Exists(ref) {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

func (s *Store) read(ref Ref, file string, into any) error {
	path := filepath.Join(s.objectDir(ref), file)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// write replaces the file atomically so a crash never leaves a partial file.
func (s *Store) write(ref Ref, file string, value any) error {
	path := filepath.Join(s.objectDir(ref), file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
