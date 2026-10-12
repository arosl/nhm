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
	// Params lists every parameter key of the variant, under the same
	// condition as Required.
	Params []string
}

// Object is the property list of one object type in the schema.
type Object struct {
	Properties []string
	Required   []string
}

// Has reports whether the object has the property.
func (o Object) Has(name string) bool {
	for _, p := range o.Properties {
		if p == name {
			return true
		}
	}
	return false
}

// Schema is the parsed subset of the pinned schema.
type Schema struct {
	Protocol      int
	SchemaVersion int
	Methods       map[string]Method
	ResultTypes   map[string]bool
	EventKinds    map[string]bool

	// Results maps a result type ("agent_info") to its object.
	Results map[string]Object
	// ResponseDefs maps a success_response $defs name ("AgentInfo") to
	// its object; defs that are not objects are left out.
	ResponseDefs map[string]Object
	// RequestDefs is the same for the request part ("AgentPromptWaitOptions").
	RequestDefs map[string]Object
	// EventData maps an event data type of the event part
	// ("pane_agent_detected") to its object.
	EventData map[string]Object
	// SubscriptionDefs is ResponseDefs for the subscription_event part
	// ("PaneAgentStatusChangedEvent").
	SubscriptionDefs map[string]Object
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
		Protocol:         doc.Protocol,
		SchemaVersion:    doc.SchemaVersion,
		Methods:          map[string]Method{},
		ResultTypes:      map[string]bool{},
		EventKinds:       map[string]bool{},
		Results:          map[string]Object{},
		ResponseDefs:     objects(res.Defs),
		RequestDefs:      objects(req.Defs),
		EventData:        map[string]Object{},
		SubscriptionDefs: map[string]Object{},
	}
	if doc.Schemas.SubscriptionEvent != nil {
		s.SubscriptionDefs = objects(doc.Schemas.SubscriptionEvent.Defs)
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
				method.Params = object(def).Properties
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
			s.Results[t.Const] = object(variant)
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
	if ev := doc.Schemas.Event; ev != nil && ev.Properties["data"] != nil {
		if data, err := resolve(ev, ev.Properties["data"].Ref); err == nil && data != nil {
			for _, variant := range data.OneOf {
				if t := variant.Properties["type"]; t != nil && t.Const != "" {
					s.EventData[t.Const] = object(variant)
				}
			}
		}
	}
	return s, nil
}

// object lists the properties and required keys of a schema node, the
// properties in sorted order.
func object(n *node) Object {
	o := Object{Required: append([]string{}, n.Required...)}
	for name := range n.Properties {
		o.Properties = append(o.Properties, name)
	}
	sort.Strings(o.Properties)
	return o
}

// objects maps each $defs entry that has properties to its Object.
func objects(defs map[string]*node) map[string]Object {
	out := map[string]Object{}
	for name, def := range defs {
		if def != nil && len(def.Properties) > 0 {
			out[name] = object(def)
		}
	}
	return out
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
