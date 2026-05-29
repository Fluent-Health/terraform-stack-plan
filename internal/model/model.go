// Package model holds the shared, budget-agnostic intermediate types that flow
// through the gather → fit → render pipeline.
package model

// Action is the reduced primary-action bucket for a resource change.
type Action string

const (
	ActionAdd     Action = "add"
	ActionChange  Action = "change"
	ActionDestroy Action = "destroy"
	ActionReplace Action = "replace"
)

// Counts holds per-stack action tallies (no-ops excluded).
type Counts struct {
	Add, Change, Destroy, Replace int
}

// Total returns the sum of all action counts.
func (c Counts) Total() int { return c.Add + c.Change + c.Destroy + c.Replace }

// AnyChange reports whether the stack has at least one non-no-op change.
func (c Counts) AnyChange() bool { return c.Total() > 0 }

// Class is the classification result for a stack.
type Class struct {
	Name string
	Icon string // "" when no glyph
}

// Label renders the class as "icon name" or just "name".
func (c Class) Label() string {
	if c.Icon == "" {
		return c.Name
	}
	return c.Icon + " " + c.Name
}

// LeafOp is the change kind for a single leaf attribute path.
type LeafOp uint8

const (
	OpAdd    LeafOp = iota // added leaf
	OpChange               // changed leaf
	OpRemove               // removed leaf
)

// Sym returns the diff prefix for the op (+, ~, -).
func (o LeafOp) Sym() string {
	switch o {
	case OpAdd:
		return "+"
	case OpRemove:
		return "-"
	default:
		return "~"
	}
}

// Leaf is one aligned `op path = value` row.
type Leaf struct {
	Op     LeafOp
	Path   string // dotted, includes the attribute name (e.g. "labels.team")
	Old    string // rendered scalar; used for change/remove
	New    string // rendered scalar; used for add/change
	Inline string // when set, rendered verbatim instead of Old/New (e.g. "(sensitive value)")
}

// Value returns the right-hand side of the `=`.
func (l Leaf) Value() string {
	if l.Inline != "" {
		return l.Inline
	}
	switch l.Op {
	case OpAdd:
		return l.New
	case OpRemove:
		return l.Old
	default:
		return l.Old + " → " + l.New
	}
}

// Field is one top-level attribute of a resource change. It renders either as
// aligned Leaves (scalars, small structural diffs, sensitive/unknown) or, when
// large, as a foldable block carrying the Variant ladder fit degrades.
type Field struct {
	Name     string
	Leaves   []Leaf    // inline rows; empty when this is a block
	Variants []Variant // block ladder; empty when this is leaves
	Selected int       // chosen variant (block only); fit mutates
}

// IsBlock reports whether this field renders as a foldable block.
func (f Field) IsBlock() bool { return len(f.Variants) > 0 }

// Sel returns the selected block variant (block fields only).
func (f Field) Sel() Variant { return f.Variants[f.Selected] }

// AtLast reports whether the selected block variant is the least-detail one.
func (f Field) AtLast() bool { return f.Selected >= len(f.Variants)-1 }

// Level identifies a render variant, ordered most → least detail.
type Level string

const (
	LevelInline     Level = "inline"     // scalar / sensitive / known-after-apply
	LevelStructural Level = "structural" // changed paths only
	LevelLineDiff   Level = "linediff"   // unified line diff
	LevelSummary    Level = "summary"    // magnitude only
	LevelHidden     Level = "hidden"     // omitted
)

// Variant is one possible rendering of a single attribute diff.
type Variant struct {
	Level   Level
	Content string // markdown fragment (lines inside the ```diff block)
	Bytes   int    // len(Content); used by fit for largest-first selection
}

// AttrDiff is one changed attribute with its ordered variants (index 0 = preferred).
type AttrDiff struct {
	Name     string
	Variants []Variant
	Selected int // chosen variant index; fit mutates this
}

// Sel returns the currently-selected variant.
func (a AttrDiff) Sel() Variant { return a.Variants[a.Selected] }

// AtLast reports whether the selected variant is already the least-detail one.
func (a AttrDiff) AtLast() bool { return a.Selected >= len(a.Variants)-1 }

// Change is one resource change within a stack.
type Change struct {
	Address string
	Type    string
	Action  Action
	Fields  []Field // populated for create/delete/update/replace
}

// Stack is one stack's parsed, classified plan.
type Stack struct {
	Name    string
	Counts  Counts
	Class   *Class // nil when classification is disabled
	Changes []Change
}

// RenderMode is the terminal-cascade level chosen by fit.
type RenderMode int

const (
	ModeFull        RenderMode = iota // table + per-stack details
	ModeSummaryOnly                   // table + notice, no details
	ModeMinimal                       // one-line aggregate + notice
)

// Report is the complete intermediate model.
type Report struct {
	Title       string
	Marker      string
	Classified  bool // whether to show the Class column
	Stacks      []Stack
	Mode        RenderMode
	Notice      string // cascade notice (set by fit when degrading)
	DetailsOpen bool   // render <details open> when true
}
