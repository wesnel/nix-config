package review

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// checkReply holds a reply to its schema, whichever engine wrote it, since
// only Codex enforces one itself. It checks the whole reply but the items
// of its findings and routes, and returns the check for one finding, so
// that a finding that does not fit is dropped alone rather than costing the
// whole reply; a route no review exists for is left out where routes are
// read.
func checkReply(schemaText, reply []byte) (func(json.RawMessage) error, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaText, &schema); err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(reply, &doc); err != nil {
		return nil, err
	}
	var items map[string]any
	if props, ok := schema["properties"].(map[string]any); ok {
		if findings, ok := props["findings"].(map[string]any); ok {
			items, _ = findings["items"].(map[string]any)
		}
	}
	if err := conforms(schema, doc, "", ".findings", ".routes"); err != nil {
		return nil, err
	}
	return func(raw json.RawMessage) error {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return conforms(items, v, "")
	}, nil
}

// conforms checks v, at the path at, against schema, in the subset of JSON
// Schema the reviewers' schemas use: type (one or a list), properties,
// required, additionalProperties false, items and enum. An array at one of
// skip is checked only for being an array.
func conforms(schema map[string]any, v any, at string, skip ...string) error {
	if schema == nil {
		return nil
	}
	where := at
	if where == "" {
		where = "the reply"
	}
	if t, ok := schema["type"]; ok {
		var types []string
		switch t := t.(type) {
		case string:
			types = []string{t}
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					types = append(types, s)
				}
			}
		}
		if !slices.ContainsFunc(types, func(t string) bool { return hasType(v, t) }) {
			return fmt.Errorf("%s is %s, not %s", where, typeOf(v), strings.Join(types, " or "))
		}
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return e == v }) {
		return fmt.Errorf("%s is %v, not one of %v", where, v, enum)
	}
	switch v := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, r := range required {
				if name, _ := r.(string); name != "" {
					if _, ok := v[name]; !ok {
						return fmt.Errorf("%s lacks %q", where, name)
					}
				}
			}
		}
		for key, value := range v {
			sub, known := props[key].(map[string]any)
			if !known {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s has %q, which its schema does not name", where, key)
				}
				continue
			}
			if err := conforms(sub, value, at+"."+key, skip...); err != nil {
				return err
			}
		}
	case []any:
		if slices.Contains(skip, at) {
			return nil
		}
		items, _ := schema["items"].(map[string]any)
		for i, x := range v {
			if err := conforms(items, x, fmt.Sprintf("%s[%d]", at, i), skip...); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasType(v any, t string) bool {
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
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return false
}

func typeOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}
