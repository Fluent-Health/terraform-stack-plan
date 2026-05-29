// Package plan parses a Terraform plan JSON document into a RawStack: reduced
// action counts plus, for update/replace changes, the set of changed attributes.
package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

// RawAttr is one changed attribute, pre-rendering.
type RawAttr struct {
	Name      string
	Before    any
	After     any
	Sensitive bool
	Unknown   bool // known after apply
}

// RawChange is one resource change with its raw Terraform actions retained
// (classify needs them) alongside the reduced bucket.
type RawChange struct {
	Address string
	Type    string
	Actions []string // raw tf actions, e.g. ["update"] or ["delete","create"]
	Action  model.Action
	Attrs   []RawAttr // populated for update/replace only
}

// RawStack is a parsed plan for one stack (no-ops excluded).
type RawStack struct {
	Name    string
	Counts  model.Counts
	Changes []RawChange
}

// Parse reads a `terraform show -json` plan document.
func Parse(name string, data []byte) (RawStack, error) {
	var p tfjson.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return RawStack{}, fmt.Errorf("stack %q: parse plan json: %w", name, err)
	}
	rs := RawStack{Name: name}
	for _, rc := range p.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		act := rc.Change.Actions
		if act.NoOp() || act.Read() || act.Forget() {
			continue
		}
		bucket := bucketOf(act)
		switch bucket {
		case model.ActionAdd:
			rs.Counts.Add++
		case model.ActionChange:
			rs.Counts.Change++
		case model.ActionDestroy:
			rs.Counts.Destroy++
		case model.ActionReplace:
			rs.Counts.Replace++
		}
		ch := RawChange{
			Address: rc.Address,
			Type:    rc.Type,
			Actions: toStrings(act),
			Action:  bucket,
		}
		if bucket == model.ActionChange || bucket == model.ActionReplace {
			ch.Attrs = changedAttrs(rc.Change)
		}
		rs.Changes = append(rs.Changes, ch)
	}
	return rs, nil
}

func bucketOf(a tfjson.Actions) model.Action {
	switch {
	case a.Replace():
		return model.ActionReplace
	case a.Create():
		return model.ActionAdd
	case a.Delete():
		return model.ActionDestroy
	default: // update
		return model.ActionChange
	}
}

func toStrings(a tfjson.Actions) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = string(x)
	}
	return out
}

// changedAttrs diffs before/after maps and returns attributes whose value
// changed, carrying sensitivity / known-after-apply flags.
func changedAttrs(c *tfjson.Change) []RawAttr {
	before, _ := c.Before.(map[string]any)
	after, _ := c.After.(map[string]any)
	unknown, _ := c.AfterUnknown.(map[string]any)
	beforeSens, _ := c.BeforeSensitive.(map[string]any)
	afterSens, _ := c.AfterSensitive.(map[string]any)

	keys := map[string]struct{}{}
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	for k := range unknown {
		keys[k] = struct{}{}
	}

	var attrs []RawAttr
	for k := range keys {
		b, a := before[k], after[k]
		isUnknown := truthy(unknown[k])
		if !isUnknown && reflect.DeepEqual(b, a) {
			continue
		}
		attrs = append(attrs, RawAttr{
			Name:      k,
			Before:    b,
			After:     a,
			Sensitive: truthy(beforeSens[k]) || truthy(afterSens[k]),
			Unknown:   isUnknown,
		})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	return attrs
}

// truthy reports whether a sensitive/unknown marker is set (Terraform encodes
// these as `true` or as a nested object; any non-nil, non-false value counts).
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	default:
		return true
	}
}
