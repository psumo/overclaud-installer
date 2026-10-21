// Package settingsmerge merges ~/.claude/settings.json three ways: the product version the
// customer last received (base), the new product version, and the customer's current file.
// New product hooks and keys land; customer edits survive; things the product dropped go,
// unless the customer changed them.
package settingsmerge

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Normalize rewrites a hook command string (e.g. python -> python3) the same way the
// installer rewrites the customer's file, so a rewritten hook still matches its product
// original. nil means identity.
type Normalize func(string) string

// Merge returns a new map; inputs are not modified. base may be nil (first install).
func Merge(base, product, customer map[string]any, norm Normalize) map[string]any {
	if norm != nil {
		base = normalizeTree(base, norm).(map[string]any)
		product = normalizeTree(product, norm).(map[string]any)
	}
	return mergeObj(base, product, customer)
}

func mergeObj(base, product, customer map[string]any) map[string]any {
	out := map[string]any{}
	keys := map[string]bool{}
	for k := range product {
		keys[k] = true
	}
	for k := range customer {
		keys[k] = true
	}
	for k := range keys {
		b, inBase := base[k]
		p, inProduct := product[k]
		c, inCustomer := customer[k]
		switch {
		case inProduct && inCustomer:
			if v, ok := mergeValue(b, inBase, p, c); ok {
				out[k] = v
			}
		case inProduct && !inCustomer:
			// Customer deleted a key the product already had: respect that.
			if !inBase {
				out[k] = clone(p)
			}
		case !inProduct && inCustomer:
			// Product dropped the key: remove it only if the customer never changed it.
			if !inBase || !reflect.DeepEqual(b, c) {
				out[k] = clone(c)
			}
		}
	}
	return out
}

func mergeValue(b any, inBase bool, p, c any) (any, bool) {
	pm, pIsObj := p.(map[string]any)
	cm, cIsObj := c.(map[string]any)
	if pIsObj && cIsObj {
		bm, _ := b.(map[string]any)
		return mergeObj(bm, pm, cm), true
	}
	pl, pIsList := p.([]any)
	cl, cIsList := c.([]any)
	if pIsList && cIsList {
		bl, _ := b.([]any)
		return mergeList(bl, pl, cl), true
	}
	switch {
	case reflect.DeepEqual(p, c):
		return clone(c), true
	case inBase && reflect.DeepEqual(b, c):
		return clone(p), true // customer left the product value alone: take the update
	default:
		return clone(c), true // customer changed it (or first install): keep theirs
	}
}

// mergeList treats lists as sets of JSON items, keeping the customer's order: drop items
// the product removed (still identical to the base), then append new product items.
func mergeList(base, product, customer []any) []any {
	out := []any{}
	for _, c := range customer {
		if contains(base, c) && !contains(product, c) {
			continue
		}
		out = append(out, clone(c))
	}
	for _, p := range product {
		if !contains(out, p) && !contains(base, p) {
			out = append(out, clone(p))
		} else if !contains(out, p) && contains(base, p) && !contains(customer, p) {
			// Customer removed a product item on purpose: leave it out.
			continue
		}
	}
	return out
}

func contains(list []any, v any) bool {
	for _, x := range list {
		if reflect.DeepEqual(x, v) {
			return true
		}
	}
	return false
}

func normalizeTree(v any, norm Normalize) any {
	switch t := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if k == "command" {
				if s, ok := x.(string); ok {
					out[k] = norm(s)
					continue
				}
			}
			out[k] = normalizeTree(x, norm)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalizeTree(x, norm)
		}
		return out
	default:
		return t
	}
}

func clone(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// PythonToPython3 is the Normalize used when the machine has python3 but no python.
func PythonToPython3(cmd string) string {
	if strings.HasPrefix(cmd, "python ") {
		return "python3 " + strings.TrimPrefix(cmd, "python ")
	}
	return cmd
}
