package uniqueness

import (
	"reflect"
	"testing"
)

// TestFlatten verifies the three shapes Flatten must handle: nested maps
// collapse to dot-joined paths, list values become the List sentinel with
// stringified elements, and scalars (including bool) pass through unchanged.
func TestFlatten(t *testing.T) {
	got := Flatten(map[string]any{
		"a": map[string]any{"b": 1},
		"c": []any{"x", "y"},
		"d": true,
	})

	want := map[string]any{
		"a.b": 1,
		"c":   List{Elems: []string{"x", "y"}},
		"d":   true,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Flatten() = %#v, want %#v", got, want)
	}
}

// TestFlattenNestedLists verifies deeper nesting (map of maps, list elements
// stringified regardless of original type) still dot-paths and sentinels
// correctly.
func TestFlattenNestedLists(t *testing.T) {
	got := Flatten(map[string]any{
		"top": map[string]any{
			"mid": map[string]any{
				"leaf":  "value",
				"nums":  []any{1, 2, 3},
				"empty": []any{},
			},
		},
	})

	want := map[string]any{
		"top.mid.leaf":  "value",
		"top.mid.nums":  List{Elems: []string{"1", "2", "3"}},
		"top.mid.empty": List{Elems: []string{}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Flatten() = %#v, want %#v", got, want)
	}
}

// TestFlattenTopLevelList verifies a list value at the top level (empty
// prefix) still produces a valid map key (empty string), mirroring the
// Python prototype's prefix="" root case.
func TestFlattenTopLevelScalar(t *testing.T) {
	got := Flatten(map[string]any{"n": 42})
	want := map[string]any{"n": 42}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Flatten() = %#v, want %#v", got, want)
	}
}
