package uniqueness

import "fmt"

// Flatten collapses a nested map (as decoded from YAML/JSON — nested
// map[string]any, []any, and scalars) into a single-level map keyed by
// dot-joined paths. A []any value becomes a List sentinel with each element
// stringified (via fmt.Sprint); any other scalar (including bool, and nil)
// passes through unchanged. It mirrors the Python prototype's `flatten`,
// which collapses lists to a ("__list__", tuple(str(x)...)) sentinel.
func Flatten(inputs map[string]any) map[string]any {
	out := map[string]any{}
	flattenInto(out, inputs, "")
	return out
}

// flattenInto recurses through obj, writing dot-path leaves into out. prefix
// is the dot-path accumulated so far ("" at the root).
func flattenInto(out map[string]any, obj any, prefix string) {
	switch v := obj.(type) {
	case map[string]any:
		for k, val := range v {
			flattenInto(out, val, joinPath(prefix, k))
		}
	case []any:
		elems := make([]string, len(v))
		for i, e := range v {
			elems[i] = fmt.Sprint(e)
		}
		out[prefix] = List{Elems: elems}
	default:
		out[prefix] = v
	}
}

// joinPath dot-joins a parent prefix and a child key, omitting the dot when
// prefix is empty (root-level key).
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
