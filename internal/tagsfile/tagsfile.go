// Package tagsfile reads and writes Mailpit tag filter files (--tags-config),
// keeping comments attached to rules that are not changed.
package tagsfile

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is one tag filter.
type Rule struct {
	Match string
	Tags  string
}

// File is a parsed tags file.
type File struct {
	doc   *yaml.Node
	items []*yaml.Node // sequence items under "filters"
	rules []Rule
}

// Parse parses a tags file. An empty file gives an empty rule list.
func Parse(data []byte) (*File, error) {
	f := &File{}
	if len(bytes.TrimSpace(data)) == 0 {
		return f, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	f.doc = &doc
	seq, err := f.filtersNode()
	if err != nil {
		return nil, err
	}
	if seq == nil {
		return f, nil
	}
	for i, it := range seq.Content {
		if it.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("filter %d: must be a mapping with match and tags", i+1)
		}
		r := Rule{}
		for j := 0; j+1 < len(it.Content); j += 2 {
			k, v := it.Content[j], it.Content[j+1]
			if v.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("filter %d: %s must be a string", i+1, k.Value)
			}
			switch k.Value {
			case "match":
				r.Match = v.Value
			case "tags":
				r.Tags = v.Value
			}
		}
		f.items = append(f.items, it)
		f.rules = append(f.rules, r)
	}
	return f, nil
}

// filtersNode returns the "filters" sequence node, or nil when missing.
func (f *File) filtersNode() (*yaml.Node, error) {
	if f.doc == nil || len(f.doc.Content) == 0 {
		return nil, nil
	}
	root := f.doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top level must be a mapping with a filters list")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "filters" {
			v := root.Content[i+1]
			if v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
				return nil, nil
			}
			if v.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("filters must be a list")
			}
			return v, nil
		}
	}
	return nil, nil
}

// Rules returns the current rules.
func (f *File) Rules() []Rule {
	return append([]Rule(nil), f.rules...)
}

// SetRules replaces all rules. Rules that did not change keep their comments.
// Comments of removed rules move to the rule that takes their place.
func (f *File) SetRules(rules []Rule) {
	used := make([]bool, len(f.items))
	newItems := make([]*yaml.Node, len(rules))
	for i, r := range rules {
		for j, old := range f.rules {
			if !used[j] && old == r {
				used[j] = true
				newItems[i] = f.items[j]
				break
			}
		}
		if newItems[i] == nil {
			newItems[i] = ruleNode(r)
		}
	}
	// move comments from removed items to the item now at that position
	for j, u := range used {
		if u || len(newItems) == 0 {
			continue
		}
		c := f.items[j].HeadComment
		if c == "" {
			continue
		}
		pos := j
		if pos >= len(newItems) {
			pos = len(newItems) - 1
		}
		if newItems[pos].HeadComment == "" {
			newItems[pos].HeadComment = c
		} else if !strings.Contains(newItems[pos].HeadComment, c) {
			newItems[pos].HeadComment = c + "\n" + newItems[pos].HeadComment
		}
	}
	f.items = newItems
	f.rules = append([]Rule(nil), rules...)
}

func ruleNode(r Rule) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "match"},
		{Kind: yaml.ScalarNode, Value: r.Match},
		{Kind: yaml.ScalarNode, Value: "tags"},
		{Kind: yaml.ScalarNode, Value: r.Tags},
	}}
}

// Bytes renders the file.
func (f *File) Bytes() ([]byte, error) {
	if f.doc == nil || len(f.doc.Content) == 0 || f.doc.Content[0].Kind != yaml.MappingNode {
		f.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := f.doc.Content[0]
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: f.items}
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "filters" {
			old := root.Content[i+1]
			seq.HeadComment, seq.LineComment, seq.FootComment = old.HeadComment, old.LineComment, old.FootComment
			root.Content[i+1] = seq
			found = true
		}
	}
	if !found {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "filters"}, seq)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(4)
	if err := enc.Encode(f.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Validate checks rules the same way Mailpit needs them.
func Validate(rules []Rule) []error {
	var errs []error
	for i, r := range rules {
		if strings.TrimSpace(r.Match) == "" {
			errs = append(errs, fmt.Errorf("rule %d: match is required", i+1))
		}
		if strings.TrimSpace(r.Tags) == "" {
			errs = append(errs, fmt.Errorf("rule %d: tags are required", i+1))
		}
		if strings.ContainsAny(r.Match+r.Tags, "\n\r") {
			errs = append(errs, fmt.Errorf("rule %d: values must be a single line", i+1))
		}
	}
	return errs
}

// ValidateBytes parses data and validates its rules.
func ValidateBytes(data []byte) []error {
	f, err := Parse(data)
	if err != nil {
		return []error{err}
	}
	// Mailpit refuses a tags file without a filters list
	if seq, _ := f.filtersNode(); seq == nil {
		return []error{fmt.Errorf("missing filters list")}
	}
	return Validate(f.rules)
}
