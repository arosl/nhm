// Package schema holds the herdr socket API schema that nhm is pinned to
// and the facts nhm reads from it: the protocol number, the request
// methods with their required parameters, the response result types and
// the event kinds.
//
// herdr-api.schema.json is the output of `herdr api schema --json` from
// herdr 0.9.3 (protocol 22, schema_version 1). Replace it only with the
// output of the herdr release that nhm moves to, never by hand.
package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed herdr-api.schema.json
var raw []byte

// Raw returns the pinned schema document as herdr printed it.
func Raw() []byte { return raw }

// Method is one request variant of the schema.
type Method struct {
	Name string
	// Required lists the parameter keys the variant requires. It is nil
	// when the parameter type is a union that this package does not walk.
	Required []string
}

// Schema is the parsed subset of the pinned schema.
type Schema struct {
	Protocol      int
	SchemaVersion int
	Methods       map[string]Method
	ResultTypes   map[string]bool
	EventKinds    map[string]bool
}

// MethodNames returns the request methods in sorted order.
func (s *Schema) MethodNames() []string {
	out := make([]string, 0, len(s.Methods))
	for name := range s.Methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

var (
	loadOnce sync.Once
	loaded   *Schema
	loadErr  error
)

// Load parses the pinned schema once.
func Load() (*Schema, error) {
	loadOnce.Do(func() { loaded, loadErr = Parse(raw) })
	return loaded, loadErr
}

// MustLoad is Load for tests and package init; it panics on error.
func MustLoad() *Schema {
	s, err := Load()
	if err != nil {
		panic(err)
	}
	return s
}

type node struct {
	Const      string           `json:"const"`
	Enum       []string         `json:"enum"`
	Ref        string           `json:"$ref"`
	Required   []string         `json:"required"`
	Properties map[string]*node `json:"properties"`
	OneOf      []*node          `json:"oneOf"`
	AnyOf      []*node          `json:"anyOf"`
	Defs       map[string]*node `json:"$defs"`
}

// UnmarshalJSON accepts the boolean schemas `true` and `false` as well as
// objects; a boolean schema has no fields nhm reads.
func (n *node) UnmarshalJSON(data []byte) error {
	if string(data) == "true" || string(data) == "false" {
		*n = node{}
		return nil
	}
	type plain node
	return json.Unmarshal(data, (*plain)(n))
}

type document struct {
	Protocol      int `json:"protocol"`
	SchemaVersion int `json:"schema_version"`
	Schemas       struct {
		Request           *node `json:"request"`
		SuccessResponse   *node `json:"success_response"`
		Event             *node `json:"event"`
		SubscriptionEvent *node `json:"subscription_event"`
	} `json:"schemas"`
}

// Parse reads a schema document in the shape `herdr api schema --json`
// prints.
func Parse(data []byte) (*Schema, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("schema: decode: %w", err)
	}
	req := doc.Schemas.Request
	res := doc.Schemas.SuccessResponse
	if doc.Protocol == 0 || req == nil || res == nil {
		return nil, fmt.Errorf("schema: protocol, request or success_response missing")
	}
	s := &Schema{
		Protocol:      doc.Protocol,
		SchemaVersion: doc.SchemaVersion,
		Methods:       map[string]Method{},
		ResultTypes:   map[string]bool{},
		EventKinds:    map[string]bool{},
	}
	for i, variant := range req.OneOf {
		m := variant.Properties["method"]
		if m == nil || m.Const == "" {
			return nil, fmt.Errorf("schema: request variant %d has no method const", i)
		}
		method := Method{Name: m.Const}
		if p := variant.Properties["params"]; p != nil {
			def, err := resolve(req, p.Ref)
			if err != nil {
				return nil, fmt.Errorf("schema: method %s: %w", m.Const, err)
			}
			if def != nil && def.OneOf == nil && def.AnyOf == nil {
				method.Required = append([]string{}, def.Required...)
			}
		}
		s.Methods[method.Name] = method
	}
	resultRef := res.Properties["result"]
	if resultRef == nil {
		return nil, fmt.Errorf("schema: success_response has no result")
	}
	result, err := resolve(res, resultRef.Ref)
	if err != nil || result == nil {
		return nil, fmt.Errorf("schema: success_response result: %v", err)
	}
	for _, variant := range result.OneOf {
		if t := variant.Properties["type"]; t != nil && t.Const != "" {
			s.ResultTypes[t.Const] = true
		}
	}
	for _, ev := range []*node{doc.Schemas.Event, doc.Schemas.SubscriptionEvent} {
		if ev == nil || ev.Properties["event"] == nil {
			continue
		}
		kind, err := resolve(ev, ev.Properties["event"].Ref)
		if err != nil || kind == nil {
			continue
		}
		for _, k := range kind.Enum {
			s.EventKinds[k] = true
		}
	}
	return s, nil
}

// resolve follows a "#/schemas/<part>/$defs/<Name>" reference inside the
// part that owns it. An empty ref resolves to nil.
func resolve(part *node, ref string) (*node, error) {
	if ref == "" {
		return nil, nil
	}
	i := strings.LastIndex(ref, "/$defs/")
	if i < 0 {
		return nil, fmt.Errorf("unsupported $ref %q", ref)
	}
	def := part.Defs[ref[i+len("/$defs/"):]]
	if def == nil {
		return nil, fmt.Errorf("unresolved $ref %q", ref)
	}
	return def, nil
}
