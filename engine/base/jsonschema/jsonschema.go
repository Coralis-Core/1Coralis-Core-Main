// Package jsonschema validates JSON documents against a deliberately SMALL,
// documented subset of JSON Schema (2020-12). Contracts may only use this
// subset, so every language can implement/verify it cheaply; the full
// reference implementations (e.g. Python's jsonschema) accept it unchanged.
//
// Supported keywords: type (single name), properties, required,
// additionalProperties (boolean), items (schema), enum (primitives only),
// minimum, maximum, minLength, maxLength, minItems.
// Annotation keywords (title, description, $comment, examples, default, $schema)
// are ignored. ANY other keyword makes Compile fail, so a contract can never
// silently rely on something this validator does not enforce.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

var annotations = map[string]bool{
	"title": true, "description": true, "$comment": true, "examples": true, "default": true, "$schema": true,
}

var supported = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true,
	"enum": true, "minimum": true, "maximum": true, "minLength": true, "maxLength": true, "minItems": true,
}

type Schema struct{ root *node }

type node struct {
	typ      string
	props    map[string]*node
	required []string
	noExtra  bool // additionalProperties: false
	items    *node
	enum     []any
	min, max *float64
	minLen   *int
	maxLen   *int
	minItems *int
}

// Compile parses and checks a schema. It fails on unsupported keywords.
func Compile(raw []byte) (*Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jsonschema: %w", err)
	}
	n, err := compile(v, "#")
	if err != nil {
		return nil, err
	}
	return &Schema{root: n}, nil
}

func compile(v any, path string) (*node, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("jsonschema: %s: schema must be an object", path)
	}
	n := &node{}
	for k := range m {
		if !supported[k] && !annotations[k] {
			return nil, fmt.Errorf("jsonschema: %s: unsupported keyword %q (contracts may only use the documented subset)", path, k)
		}
	}
	if t, ok := m["type"]; ok {
		s, isStr := t.(string)
		switch {
		case !isStr:
			return nil, fmt.Errorf("jsonschema: %s: type must be a single string", path)
		case s != "object" && s != "array" && s != "string" && s != "integer" && s != "number" && s != "boolean" && s != "null":
			return nil, fmt.Errorf("jsonschema: %s: unknown type %q", path, s)
		}
		n.typ = s
	}
	if p, ok := m["properties"]; ok {
		pm, isMap := p.(map[string]any)
		if !isMap {
			return nil, fmt.Errorf("jsonschema: %s: properties must be an object", path)
		}
		n.props = map[string]*node{}
		for name, sub := range pm {
			c, err := compile(sub, path+"/properties/"+name)
			if err != nil {
				return nil, err
			}
			n.props[name] = c
		}
	}
	if r, ok := m["required"]; ok {
		arr, isArr := r.([]any)
		if !isArr {
			return nil, fmt.Errorf("jsonschema: %s: required must be an array", path)
		}
		for _, e := range arr {
			s, isStr := e.(string)
			if !isStr {
				return nil, fmt.Errorf("jsonschema: %s: required entries must be strings", path)
			}
			n.required = append(n.required, s)
		}
	}
	if a, ok := m["additionalProperties"]; ok {
		b, isBool := a.(bool)
		if !isBool {
			return nil, fmt.Errorf("jsonschema: %s: additionalProperties must be a boolean", path)
		}
		n.noExtra = !b
	}
	if it, ok := m["items"]; ok {
		c, err := compile(it, path+"/items")
		if err != nil {
			return nil, err
		}
		n.items = c
	}
	if e, ok := m["enum"]; ok {
		arr, isArr := e.([]any)
		if !isArr || len(arr) == 0 {
			return nil, fmt.Errorf("jsonschema: %s: enum must be a non-empty array", path)
		}
		for _, x := range arr {
			switch x.(type) {
			case string, bool, nil, json.Number:
			default:
				return nil, fmt.Errorf("jsonschema: %s: enum supports primitives only", path)
			}
		}
		n.enum = arr
	}
	var err error
	if n.min, err = num(m, "minimum", path); err != nil {
		return nil, err
	}
	if n.max, err = num(m, "maximum", path); err != nil {
		return nil, err
	}
	for _, spec := range []struct {
		key string
		dst **int
	}{{"minLength", &n.minLen}, {"maxLength", &n.maxLen}, {"minItems", &n.minItems}} {
		if v, ok := m[spec.key]; ok {
			f, ok := toFloat(v)
			if !ok || f < 0 || f != math.Trunc(f) {
				return nil, fmt.Errorf("jsonschema: %s: %s must be a non-negative integer", path, spec.key)
			}
			i := int(f)
			*spec.dst = &i
		}
	}
	return n, nil
}

func num(m map[string]any, key, path string) (*float64, error) {
	v, ok := m[key]
	if !ok {
		return nil, nil
	}
	f, isNum := toFloat(v)
	if !isNum {
		return nil, fmt.Errorf("jsonschema: %s: %s must be a number", path, key)
	}
	return &f, nil
}

func toFloat(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// Validate checks a JSON document. The returned error lists every violation
// with a JSON-pointer-like path.
func (s *Schema) Validate(doc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("jsonschema: invalid JSON: %w", err)
	}
	if dec.More() {
		return errors.New("jsonschema: trailing data after JSON value")
	}
	var errs []string
	validate(s.root, v, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return errors.New(strings.Join(errs, "; "))
}

func at(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func validate(n *node, v any, path string, errs *[]string) {
	add := func(format string, a ...any) { *errs = append(*errs, at(path)+": "+fmt.Sprintf(format, a...)) }

	if n.typ != "" && !typeOK(n.typ, v) {
		add("expected %s, got %s", n.typ, kind(v))
		return
	}
	if len(n.enum) > 0 {
		found := false
		for _, e := range n.enum {
			if equalPrim(e, v) {
				found = true
				break
			}
		}
		if !found {
			add("value not in enum")
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for _, r := range n.required {
			if _, ok := x[r]; !ok {
				add("missing required property %q", r)
			}
		}
		for k, val := range x {
			if sub, ok := n.props[k]; ok {
				validate(sub, val, path+"/"+k, errs)
			} else if n.noExtra {
				add("unexpected property %q", k)
			}
		}
	case []any:
		if n.minItems != nil && len(x) < *n.minItems {
			add("needs at least %d items", *n.minItems)
		}
		if n.items != nil {
			for i, e := range x {
				validate(n.items, e, fmt.Sprintf("%s/%d", path, i), errs)
			}
		}
	case string:
		l := utf8.RuneCountInString(x)
		if n.minLen != nil && l < *n.minLen {
			add("shorter than minLength %d", *n.minLen)
		}
		if n.maxLen != nil && l > *n.maxLen {
			add("longer than maxLength %d", *n.maxLen)
		}
	case json.Number:
		f, _ := x.Float64()
		if n.min != nil && f < *n.min {
			add("below minimum %v", *n.min)
		}
		if n.max != nil && f > *n.max {
			add("above maximum %v", *n.max)
		}
	}
}

func typeOK(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
	}
	return false
}

func kind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case json.Number:
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

func equalPrim(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok || bok {
		if !(aok && bok) {
			return false
		}
		af, _ := an.Float64()
		bf, _ := bn.Float64()
		return af == bf
	}
	return a == b
}
