// Package plan parses a Terraform plan JSON document into a RawStack: reduced
// action counts plus the set of affected attributes for each change action.
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
	Sensitive bool // the WHOLE attribute is sensitive (marker is a bare `true`)
	Unknown   bool // known after apply
	// BeforeSensitive/AfterSensitive carry Terraform's per-path sensitivity tree
	// for this attribute when it is a nested map/list (not a bare bool), so the
	// differ can redact only the sensitive sub-paths instead of the whole value.
	BeforeSensitive any
	AfterSensitive  any
}

// RawChange is one resource change with its raw Terraform actions retained
// (classify needs them) alongside the reduced bucket.
type RawChange struct {
	Address string
	Type    string
	Actions []string // raw tf actions, e.g. ["update"] or ["delete","create"]
	Action  model.Action
	Attrs   []RawAttr // populated for create/delete/update/replace/forget

	Name          string
	ModuleAddress string

	// Raw holds top-level scalar attributes (string/number/bool), after over
	// before, sensitive values skipped. Retained for classification attribute
	// extraction, which must see attributes even when they did not change.
	Raw map[string]any

	Moved           bool
	PreviousAddress string
	Imported        bool
	ImportID        string
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
		if act.Read() {
			continue
		}
		moved := rc.PreviousAddress != "" && rc.PreviousAddress != rc.Address
		imported := rc.Change.Importing != nil
		// Pure no-op (no move, no import) carries nothing to show.
		if act.NoOp() && !moved && !imported {
			continue
		}

		var bucket model.Action
		switch {
		case act.Forget():
			bucket = model.ActionForget
		case act.NoOp():
			bucket = model.ActionNoop // move/import only
		default:
			bucket = bucketOf(act)
		}

		switch bucket {
		case model.ActionAdd:
			rs.Counts.Add++
		case model.ActionChange:
			rs.Counts.Change++
		case model.ActionDestroy:
			rs.Counts.Destroy++
		case model.ActionReplace:
			rs.Counts.Replace++
		case model.ActionForget:
			rs.Counts.Forget++
		}
		if moved {
			rs.Counts.Move++
		}
		if imported {
			rs.Counts.Import++
		}

		ch := RawChange{
			Address:       rc.Address,
			Type:          rc.Type,
			Actions:       toStrings(act),
			Action:        bucket,
			Moved:         moved,
			Imported:      imported,
			Name:          rc.Name,
			ModuleAddress: rc.ModuleAddress,
			Raw:           rawScalars(rc.Change),
		}
		if moved {
			ch.PreviousAddress = rc.PreviousAddress
		}
		if imported {
			ch.ImportID = rc.Change.Importing.ID
		}
		switch bucket {
		case model.ActionChange, model.ActionReplace:
			ch.Attrs = changedAttrs(rc.Change)
		case model.ActionAdd:
			ch.Attrs = sideAttrs(rc.Change, true)
		case model.ActionDestroy, model.ActionForget:
			ch.Attrs = sideAttrs(rc.Change, false)
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
			Name:            k,
			Before:          b,
			After:           a,
			Sensitive:       isTrue(beforeSens[k]) || isTrue(afterSens[k]),
			Unknown:         isUnknown,
			BeforeSensitive: beforeSens[k],
			AfterSensitive:  afterSens[k],
		})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	return attrs
}

// sideAttrs lists every leaf attribute of a create (after=true) or delete
// (after=false), carrying sensitive / known-after-apply markers.
func sideAttrs(c *tfjson.Change, after bool) []RawAttr {
	src, _ := c.After.(map[string]any)
	sens, _ := c.AfterSensitive.(map[string]any)
	unknown, _ := c.AfterUnknown.(map[string]any)
	if !after {
		src, _ = c.Before.(map[string]any)
		sens, _ = c.BeforeSensitive.(map[string]any)
		unknown = nil // deletes have no after_unknown
	}

	keys := map[string]struct{}{}
	for k := range src {
		keys[k] = struct{}{}
	}
	for k := range unknown {
		keys[k] = struct{}{}
	}

	var attrs []RawAttr
	for k := range keys {
		ra := RawAttr{Name: k, Sensitive: isTrue(sens[k]), Unknown: truthy(unknown[k])}
		if after {
			ra.After = src[k]
			ra.AfterSensitive = sens[k]
		} else {
			ra.Before = src[k]
			ra.BeforeSensitive = sens[k]
		}
		attrs = append(attrs, ra)
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

// isTrue reports whether a marker means the WHOLE attribute is sensitive — i.e.
// Terraform encoded it as a bare `true`. A nested object/array marks only deep
// leaves; those are carried as a subtree and redacted per-path by the differ,
// NOT collapsed into whole-attribute sensitivity (which would hide every
// sibling, e.g. a cpu-request change next to a sensitive env var).
func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// rawScalars returns the change's top-level scalar attributes (string, bool,
// number), preferring after over before. It skips any attribute flagged
// sensitive on either side, and any attribute that is known-after-apply
// (computed), so Raw never surfaces a sensitive or not-yet-known value. Used by
// classification attribute extraction, which must see an attribute even when it
// did not change (changedAttrs keeps only differing values).
func rawScalars(c *tfjson.Change) map[string]any {
	after, _ := c.After.(map[string]any)
	before, _ := c.Before.(map[string]any)
	afterSens, _ := c.AfterSensitive.(map[string]any)
	beforeSens, _ := c.BeforeSensitive.(map[string]any)
	unknown, _ := c.AfterUnknown.(map[string]any)

	out := map[string]any{}
	put := func(src map[string]any) {
		for k, v := range src {
			if _, ok := out[k]; ok {
				continue // after already won
			}
			if truthy(afterSens[k]) || truthy(beforeSens[k]) || truthy(unknown[k]) {
				continue // never surface a sensitive or computed value
			}
			if isScalar(v) {
				out[k] = v
			}
		}
	}
	put(after)
	put(before)
	if len(out) == 0 {
		return nil
	}
	return out
}

// isScalar reports whether v is a JSON scalar we can retain in Raw.
func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}
