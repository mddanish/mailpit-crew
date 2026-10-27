// Package yamlmap edits flat YAML mappings (key: value), such as Mailpit's
// SMTP relay and forward files, keeping comments and key order.
package yamlmap

import (
	"bytes"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Entry is one key in the file.
type Entry struct {
	Key   string
	Value string
	Line  int
}

// File is a parsed mapping.
type File struct {
	doc *yaml.Node
}

// Parse parses data. Empty data gives an empty mapping.
func Parse(data []byte) (*File, error) {
	f := &File{}
	if len(bytes.TrimSpace(data)) == 0 {
		f.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		return f, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top level must be a mapping of key: value")
	}
	f.doc = &doc
	return f, nil
}

func (f *File) root() *yaml.Node { return f.doc.Content[0] }

// Entries returns all keys in file order.
func (f *File) Entries() []Entry {
	var out []Entry
	r := f.root()
	for i := 0; i+1 < len(r.Content); i += 2 {
		k, v := r.Content[i], r.Content[i+1]
		out = append(out, Entry{Key: k.Value, Value: v.Value, Line: k.Line})
	}
	return out
}

// Get returns the value of key.
func (f *File) Get(key string) (string, bool) {
	r := f.root()
	for i := 0; i+1 < len(r.Content); i += 2 {
		if r.Content[i].Value == key {
			return r.Content[i+1].Value, true
		}
	}
	return "", false
}

// Kind tells Set how to write a value.
type Kind int

// Value kinds.
const (
	String Kind = iota
	Bool
	Int
)

// Set adds or replaces key. Comments on an existing key are kept.
func (f *File) Set(key, value string, kind Kind) error {
	v := &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"}
	switch kind {
	case Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be true or false", key)
		}
		v.Tag, v.Value = "!!bool", strconv.FormatBool(b)
	case Int:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%s must be a whole number", key)
		}
		v.Tag = "!!int"
	}
	r := f.root()
	for i := 0; i+1 < len(r.Content); i += 2 {
		if r.Content[i].Value == key {
			old := r.Content[i+1]
			v.LineComment, v.HeadComment, v.FootComment = old.LineComment, old.HeadComment, old.FootComment
			r.Content[i+1] = v
			return nil
		}
	}
	r.Content = append(r.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}, v)
	return nil
}

// Delete removes key and reports whether it existed.
func (f *File) Delete(key string) bool {
	r := f.root()
	for i := 0; i+1 < len(r.Content); i += 2 {
		if r.Content[i].Value == key {
			r.Content = append(r.Content[:i], r.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Bytes renders the file.
func (f *File) Bytes() ([]byte, error) {
	if len(f.root().Content) == 0 && f.root().HeadComment == "" && f.root().FootComment == "" {
		return []byte{}, nil
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
