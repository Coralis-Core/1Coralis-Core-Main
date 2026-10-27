// Package contract loads language-neutral contract files (*.contract.json) and
// enforces them: it self-checks each file, validates payloads against the
// JSON Schemas inside, and offers a conformance check any provider (in any
// language, behind any transport) can be run through.
//
// It never embeds domain contracts itself (those live in the layer above):
// callers pass an fs.FS.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"example.com/project/engine/base/jsonschema"
)

const (
	KindRequest = "request"
	KindEvent   = "event"
)

type Example struct {
	Name    string
	Input   json.RawMessage
	Output  json.RawMessage
	Payload json.RawMessage
	Error   string
	// Match: "" or "contains" = provider output must contain the example output;
	// "schema" = provider output only has to satisfy the output schema (for
	// outputs that are inherently provider-specific, e.g. a core's own description).
	Match string
}

// Invalid is a negative example: a document that MUST be rejected.
type Invalid struct {
	Name string
	Doc  json.RawMessage
}

type Contract struct {
	Capability string
	Version    string
	Kind       string
	Summary    string
	// Idempotent declares that repeating the request is safe (default false).
	// Retry or backup AFTER the request was sent happens only if true.
	Idempotent     bool
	Errors         []string
	Examples       []Example
	InvalidInputs  []Invalid // request contracts
	InvalidPayload []Invalid // event contracts

	input, output, payload *jsonschema.Schema
}

type Set struct{ byName map[string]*Contract }

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*\.v([0-9]+)(\.[a-z][a-z0-9_]*)+$`)

type file struct {
	Capability string            `json:"capability"`
	Version    string            `json:"version"`
	Kind       string            `json:"kind"`
	Summary    string            `json:"summary"`
	Idempotent bool              `json:"idempotent"`
	Input      json.RawMessage   `json:"input"`
	Output     json.RawMessage   `json:"output"`
	Payload    json.RawMessage   `json:"payload"`
	Errors     []string          `json:"errors"`
	Examples   []json.RawMessage `json:"examples"`

	InvalidInputs   []invalidFile `json:"invalid_inputs"`
	InvalidPayloads []invalidFile `json:"invalid_payloads"`
}

type invalidFile struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Payload json.RawMessage `json:"payload"`
}

// Generic codes any provider/transport may return in addition to the
// contract's own domain codes.
var GenericCodes = []string{"unavailable", "maybe_delivered", "invalid_input", "internal", "depth_exceeded"}

// LoadAll loads and verifies the contracts of several filesystems into one set
// (e.g. the structural contracts plus your own). A capability defined twice is an error.
func LoadAll(fsyss ...fs.FS) (*Set, error) {
	all := &Set{byName: map[string]*Contract{}}
	var errs []error
	for _, f := range fsyss {
		s, err := Load(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, name := range s.Names() {
			if _, dup := all.byName[name]; dup {
				errs = append(errs, fmt.Errorf("contract %q defined in more than one place", name))
				continue
			}
			c, _ := s.Get(name)
			all.byName[name] = c
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return all, nil
}

// Load reads every *.contract.json under fsys and verifies it.
func Load(fsys fs.FS) (*Set, error) {
	s := &Set{byName: map[string]*Contract{}}
	var errs []error
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".contract.json") {
			return nil
		}
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		c, err := parse(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		if _, dup := s.byName[c.Capability]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate contract %q", path, c.Capability))
			return nil
		}
		s.byName[c.Capability] = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

func parse(raw []byte) (*Contract, error) {
	var f file
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	m := namePattern.FindStringSubmatch(f.Capability)
	if m == nil {
		return nil, fmt.Errorf("bad capability name %q (want domain.vN.operation)", f.Capability)
	}
	if !strings.HasPrefix(f.Version, m[1]+".") {
		return nil, fmt.Errorf("version %q does not match the major version in %q", f.Version, f.Capability)
	}
	c := &Contract{Capability: f.Capability, Version: f.Version, Kind: f.Kind, Summary: f.Summary, Idempotent: f.Idempotent, Errors: f.Errors}
	var err error
	switch f.Kind {
	case KindRequest:
		if c.input, err = jsonschema.Compile(f.Input); err != nil {
			return nil, fmt.Errorf("input: %w", err)
		}
		if c.output, err = jsonschema.Compile(f.Output); err != nil {
			return nil, fmt.Errorf("output: %w", err)
		}
	case KindEvent:
		if c.payload, err = jsonschema.Compile(f.Payload); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
	default:
		return nil, fmt.Errorf("kind must be %q or %q", KindRequest, KindEvent)
	}
	if len(f.Examples) == 0 {
		return nil, errors.New("at least one example is required (examples are the conformance fixtures)")
	}
	for _, rawEx := range f.Examples {
		var e struct {
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Output  json.RawMessage `json:"output"`
			Payload json.RawMessage `json:"payload"`
			Error   string          `json:"error"`
			Match   string          `json:"match"`
		}
		if err := json.Unmarshal(rawEx, &e); err != nil {
			return nil, err
		}
		ex := Example{Name: e.Name, Input: e.Input, Output: e.Output, Payload: e.Payload, Error: e.Error, Match: e.Match}
		if err := c.checkExample(ex); err != nil {
			return nil, fmt.Errorf("example %q: %w", ex.Name, err)
		}
		c.Examples = append(c.Examples, ex)
	}
	for _, in := range f.InvalidInputs {
		if c.Kind != KindRequest {
			return nil, errors.New("invalid_inputs is only for request contracts")
		}
		if err := c.ValidateInput(in.Input); err == nil {
			return nil, fmt.Errorf("invalid input %q is actually valid: negative examples must violate the schema", in.Name)
		}
		c.InvalidInputs = append(c.InvalidInputs, Invalid{Name: in.Name, Doc: in.Input})
	}
	for _, in := range f.InvalidPayloads {
		if c.Kind != KindEvent {
			return nil, errors.New("invalid_payloads is only for event contracts")
		}
		if err := c.ValidatePayload(in.Payload); err == nil {
			return nil, fmt.Errorf("invalid payload %q is actually valid: negative examples must violate the schema", in.Name)
		}
		c.InvalidPayload = append(c.InvalidPayload, Invalid{Name: in.Name, Doc: in.Payload})
	}
	return c, nil
}

func (c *Contract) checkExample(e Example) error {
	if c.Kind == KindEvent {
		if len(e.Payload) == 0 {
			return errors.New("event example needs a payload")
		}
		return c.ValidatePayload(e.Payload)
	}
	if len(e.Input) == 0 {
		return errors.New("request example needs an input")
	}
	switch e.Match {
	case "", "contains", "schema":
	default:
		return fmt.Errorf("match must be \"contains\" or \"schema\" (got %q)", e.Match)
	}
	if e.Match == "schema" && e.Error != "" {
		return errors.New(`match "schema" cannot be combined with an error example`)
	}
	if err := c.ValidateInput(e.Input); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	switch {
	case e.Error != "":
		if !contains(c.Errors, e.Error) {
			return fmt.Errorf("error code %q is not declared in errors", e.Error)
		}
	case len(e.Output) > 0:
		if err := c.ValidateOutput(e.Output); err != nil {
			return fmt.Errorf("output: %w", err)
		}
	default:
		return errors.New("needs an output or an error")
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Declares reports whether code may leave this capability: a code its contract
// declares, or one of the generic codes every capability may use.
func (c *Contract) Declares(code string) bool {
	return contains(c.Errors, code) || contains(GenericCodes, code)
}

func (c *Contract) ValidateInput(doc []byte) error  { return need(c.input, "input").Validate(doc) }
func (c *Contract) ValidateOutput(doc []byte) error { return need(c.output, "output").Validate(doc) }
func (c *Contract) ValidatePayload(doc []byte) error {
	return need(c.payload, "payload").Validate(doc)
}

// need avoids nil-pointer panics when a method is used on the wrong kind.
func need(s *jsonschema.Schema, what string) *schemaOrErr {
	return &schemaOrErr{s, what}
}

type schemaOrErr struct {
	s    *jsonschema.Schema
	what string
}

func (x *schemaOrErr) Validate(doc []byte) error {
	if x.s == nil {
		return fmt.Errorf("contract has no %s schema (wrong kind)", x.what)
	}
	return x.s.Validate(doc)
}

func (s *Set) Get(name string) (*Contract, bool) { c, ok := s.byName[name]; return c, ok }

func (s *Set) Has(name string) bool { _, ok := s.byName[name]; return ok }

func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---- conformance -----------------------------------------------------------

// Provider is the language-neutral view of a capability implementation: JSON
// in, JSON out (or a contract error code). A Go core, a Python process behind
// a pipe, or an HTTP service can all be wrapped as a Provider.
type Provider func(ctx context.Context, input []byte) (output []byte, errCode string, err error)

// Check runs every example of a request contract through p and returns the
// list of problems (empty = conformant). An example's output is a MINIMUM
// guarantee: every field it lists must be present and equal, but a provider
// may return additional (optional, schema-valid) fields. This is what lets very
// different cores (e.g. one that always reports a default language) satisfy
// the same contract, and it matches the v1 rule "only optional fields get added".
func Check(ctx context.Context, c *Contract, p Provider) []string {
	return check(ctx, c, p, false)
}

// CheckExact is Check with exact output equality (no extra fields allowed).
func CheckExact(ctx context.Context, c *Contract, p Provider) []string {
	return check(ctx, c, p, true)
}

func check(ctx context.Context, c *Contract, p Provider, exact bool) []string {
	if c.Kind != KindRequest {
		return []string{"conformance applies to request contracts only"}
	}
	var problems []string
	for _, ex := range c.Examples {
		out, code, err := p(ctx, ex.Input)
		fail := func(f string, a ...any) {
			problems = append(problems, fmt.Sprintf("%s/%s: ", c.Capability, ex.Name)+fmt.Sprintf(f, a...))
		}
		if err != nil {
			fail("provider failed: %v", err)
			continue
		}
		if ex.Error != "" {
			if code != ex.Error {
				fail("want error code %q, got %q", ex.Error, code)
			}
			continue
		}
		if code != "" {
			fail("unexpected error code %q", code)
			continue
		}
		if verr := c.ValidateOutput(out); verr != nil {
			fail("output violates schema: %v", verr)
			continue
		}
		if ex.Match == "schema" {
			continue // the schema check above is the whole assertion
		}
		match := jsonContains
		if exact {
			match = jsonEqual
		}
		if !match(out, ex.Output) {
			fail("output %s does not match example %s (exact=%v)", out, ex.Output, exact)
		}
	}
	for _, bad := range c.InvalidInputs {
		_, code, err := p(ctx, bad.Doc)
		if code != "invalid_input" && err == nil {
			problems = append(problems, fmt.Sprintf("%s/invalid:%s: provider accepted an input the contract forbids (got code %q)", c.Capability, bad.Name, code))
		}
	}
	return problems
}

// CheckEmitted verifies what a PUBLISHER actually put on the wire: at least
// one payload, and every payload must satisfy the schema.
func CheckEmitted(c *Contract, emitted [][]byte) []string {
	if c.Kind != KindEvent {
		return []string{"CheckEmitted applies to event contracts only"}
	}
	if len(emitted) == 0 {
		return []string{c.Capability + ": publisher emitted nothing"}
	}
	var problems []string
	for i, raw := range emitted {
		if err := c.ValidatePayload(raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: emitted payload #%d violates schema: %v", c.Capability, i, err))
		}
	}
	return problems
}

// CheckSubscriber verifies a SUBSCRIBER: every valid example payload must be
// accepted (no error) and every negative payload must be rejected (error).
func CheckSubscriber(ctx context.Context, c *Contract, deliver func(ctx context.Context, payload []byte) error) []string {
	if c.Kind != KindEvent {
		return []string{"CheckSubscriber applies to event contracts only"}
	}
	var problems []string
	for _, ex := range c.Examples {
		if err := deliver(ctx, ex.Payload); err != nil {
			problems = append(problems, fmt.Sprintf("%s/%s: subscriber rejected a valid payload: %v", c.Capability, ex.Name, err))
		}
	}
	for _, bad := range c.InvalidPayload {
		if err := deliver(ctx, bad.Doc); err == nil {
			problems = append(problems, fmt.Sprintf("%s/invalid:%s: subscriber accepted a payload the contract forbids", c.Capability, bad.Name))
		}
	}
	return problems
}

// jsonContains reports whether got contains want: objects are compared
// field-by-field recursively (extra fields in got are fine); everything else
// must be equal.
func jsonContains(got, want []byte) bool {
	var g, w any
	if json.Unmarshal(got, &g) != nil || json.Unmarshal(want, &w) != nil {
		return false
	}
	return contains2(g, w)
}

func contains2(g, w any) bool {
	wm, wIsObj := w.(map[string]any)
	if !wIsObj {
		return reflect.DeepEqual(g, w)
	}
	gm, gIsObj := g.(map[string]any)
	if !gIsObj {
		return false
	}
	for k, wv := range wm {
		gv, ok := gm[k]
		if !ok || !contains2(gv, wv) {
			return false
		}
	}
	return true
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// JSONAdapter wraps a typed Go function as a Provider. codeOf maps a Go error
// to a contract error code ("" = not a contract error, returned as failure).
func JSONAdapter[In, Out any](fn func(context.Context, In) (Out, error), codeOf func(error) string) Provider {
	return func(ctx context.Context, input []byte) ([]byte, string, error) {
		var in In
		dec := json.NewDecoder(bytes.NewReader(input))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, "invalid_input", nil
		}
		out, err := fn(ctx, in)
		if err != nil {
			if code := codeOf(err); code != "" {
				return nil, code, nil
			}
			return nil, "", err
		}
		b, err := json.Marshal(out)
		return b, "", err
	}
}

// Guard wraps a Provider so the contract is enforced at the boundary: input is
// validated against the input schema before the provider sees it (violations
// become the code "invalid_input"), output against the output schema before
// the caller sees it, and only declared or generic error codes may escape.
// Go's JSON decoder is case-insensitive and ignores missing required fields;
// the schema is the only strict judge, so every transport adapter exposes
// providers through Guard.
func Guard(c *Contract, p Provider) Provider {
	return func(ctx context.Context, input []byte) ([]byte, string, error) {
		if err := c.ValidateInput(input); err != nil {
			return nil, "invalid_input", nil
		}
		out, code, err := p(ctx, input)
		if err != nil {
			return nil, "", err
		}
		if code != "" {
			if !contains(c.Errors, code) && !contains(GenericCodes, code) {
				return nil, "", fmt.Errorf("%s: provider returned undeclared error code %q", c.Capability, code)
			}
			return nil, code, nil
		}
		if err := c.ValidateOutput(out); err != nil {
			return nil, "", fmt.Errorf("%s: provider output violates contract: %w", c.Capability, err)
		}
		return out, "", nil
	}
}
