package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// change is one entry in a Terraform plan's resource_changes array. The helpers
// below build deterministic plan JSON for the golden example tests — no time or
// randomness, so re-running with -update yields byte-identical output.
type change map[string]any

func emptyObj() map[string]any { return map[string]any{} }

func resourceChange(addr, typ string, ch map[string]any) change {
	name := addr
	if i := strings.LastIndex(addr, "."); i >= 0 {
		name = addr[i+1:]
	}
	return change{"address": addr, "type": typ, "name": name, "change": ch}
}

func create(addr, typ string) change {
	return resourceChange(addr, typ, map[string]any{
		"actions": []string{"create"}, "before": nil,
		"after":         map[string]any{"id": addr},
		"after_unknown": emptyObj(), "before_sensitive": emptyObj(), "after_sensitive": emptyObj(),
	})
}

func del(addr, typ string) change {
	return resourceChange(addr, typ, map[string]any{
		"actions": []string{"delete"}, "after": nil,
		"before":        map[string]any{"id": addr},
		"after_unknown": emptyObj(), "before_sensitive": emptyObj(), "after_sensitive": emptyObj(),
	})
}

func replace(addr, typ string, before, after map[string]any) change {
	return resourceChange(addr, typ, map[string]any{
		"actions": []string{"delete", "create"}, "before": before, "after": after,
		"after_unknown": emptyObj(), "before_sensitive": emptyObj(), "after_sensitive": emptyObj(),
	})
}

func update(addr, typ string, before, after map[string]any) change {
	return resourceChange(addr, typ, map[string]any{
		"actions": []string{"update"}, "before": before, "after": after,
		"after_unknown": emptyObj(), "before_sensitive": emptyObj(), "after_sensitive": emptyObj(),
	})
}

// structuralUpdate changes a nested map, so the differ emits a structural
// (changed-paths-only) diff. n perturbs the values to keep addresses distinct.
func structuralUpdate(addr string, n int) change {
	before := map[string]any{
		"labels":         map[string]any{"env": "nonprod"},
		"retention_days": float64(7 + n),
	}
	after := map[string]any{
		"labels":         map[string]any{"env": "nonprod", "team": "platform"},
		"retention_days": float64(30 + n),
	}
	return update(addr, "google_storage_bucket", before, after)
}

// iamUpdate is an IAM role change — matched by the built-in `iam` preset.
func iamUpdate(addr string) change {
	return update(addr, "google_project_iam_member",
		map[string]any{"role": "roles/viewer"},
		map[string]any{"role": "roles/editor"})
}

// sensitiveUpdate marks the changed attribute sensitive; it renders redacted.
func sensitiveUpdate(addr string) change {
	return resourceChange(addr, "google_secret_manager_secret_version", map[string]any{
		"actions":          []string{"update"},
		"before":           map[string]any{"secret_data": "old"},
		"after":            map[string]any{"secret_data": "new"},
		"after_unknown":    emptyObj(),
		"before_sensitive": map[string]any{"secret_data": true},
		"after_sensitive":  map[string]any{"secret_data": true},
	})
}

// yamlUpdate changes a few keys of a YAML string attribute — the structural
// (changed-paths-only) case. changed controls how many keys differ (few →
// inline structural leaves, many → folded block).
func yamlUpdate(addr string, changed int) change {
	var before, after strings.Builder
	before.WriteString("spec:\n")
	after.WriteString("spec:\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&before, "  key_%02d: old\n", i)
		v := "old"
		if i < changed {
			v = "new"
		}
		fmt.Fprintf(&after, "  key_%02d: %s\n", i, v)
	}
	return update(addr, "kubernetes_manifest",
		map[string]any{"manifest": before.String()},
		map[string]any{"manifest": after.String()})
}

// bigUpdate produces an update whose attribute is a large multi-line string,
// giving `fit` a big line-diff variant it can degrade to a summary line. The
// resource type deliberately avoids "iam" so it doesn't perturb classification.
func bigUpdate(addr string, lines int) change {
	var before, after strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&before, "statement_%04d allow action old\n", i)
		fmt.Fprintf(&after, "statement_%04d allow action new\n", i)
	}
	return update(addr, "kubernetes_config_map",
		map[string]any{"data": before.String()},
		map[string]any{"data": after.String()})
}

// genPlan marshals changes into a `terraform show -json`-shaped document.
func genPlan(changes ...change) []byte {
	rc := make([]change, len(changes))
	copy(rc, changes)
	doc := map[string]any{"format_version": "1.2", "resource_changes": rc}
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		panic(err)
	}
	return b
}
