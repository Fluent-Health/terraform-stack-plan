//! The budget-agnostic intermediate types that flow through gather, fit and render
//! (`internal/model`, with the canonical `internal/domain` types it aliases).

/// The reduced primary-action bucket for a resource change.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    /// Create.
    Add,
    /// Update in place.
    Change,
    /// Delete.
    Destroy,
    /// Delete and create.
    Replace,
    /// Removed from state, real resource kept.
    Forget,
    /// No config change; carries a move or import annotation.
    Noop,
}

impl Action {
    /// Whether the action changes the real resource (create, update, delete, replace), as
    /// opposed to pure state bookkeeping (move, import, forget).
    pub fn mutates(self) -> bool {
        matches!(self, Action::Add | Action::Change | Action::Destroy | Action::Replace)
    }
}

/// Per-stack action tallies.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Counts {
    /// Creates.
    pub add: i64,
    /// Updates.
    pub change: i64,
    /// Deletes.
    pub destroy: i64,
    /// Replaces.
    pub replace: i64,
    /// Moves.
    pub moved: i64,
    /// Imports.
    pub import: i64,
    /// Forgets.
    pub forget: i64,
    /// Updates that only change sensitivity.
    pub sensitivity_only: i64,
}

impl Counts {
    /// The sum of the create, change, destroy and replace buckets.
    pub fn total(&self) -> i64 {
        self.add + self.change + self.destroy + self.replace
    }

    /// Whether the stack has anything worth rendering: a config change, a move, an import or a
    /// forget.
    pub fn any_change(&self) -> bool {
        self.total() + self.moved + self.import + self.forget > 0
    }
}

/// A matched classification rule: name, optional glyph, and the sorted-unique values of the
/// attributes it emits. `attributes` is `None` when nothing was emitted.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Category {
    /// Rule name.
    pub name: String,
    /// Optional glyph.
    pub icon: String,
    /// Emitted attribute values, by attribute name in sorted order.
    pub attributes: Option<std::collections::BTreeMap<String, Vec<String>>>,
}

impl Category {
    /// "icon name", or just "name".
    pub fn label(&self) -> String {
        if self.icon.is_empty() {
            self.name.clone()
        } else {
            format!("{} {}", self.icon, self.name)
        }
    }
}

/// The change kind of one leaf attribute path.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LeafOp {
    /// Added leaf.
    Add,
    /// Changed leaf.
    Change,
    /// Removed leaf.
    Remove,
}

impl LeafOp {
    /// The diff prefix: `+`, `~` or `-`.
    pub fn sym(self) -> &'static str {
        match self {
            LeafOp::Add => "+",
            LeafOp::Remove => "-",
            LeafOp::Change => "~",
        }
    }
}

/// One aligned `op path = value` row.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Leaf {
    /// Op.
    pub op: LeafOp,
    /// Dotted path including the attribute name.
    pub path: String,
    /// Rendered old scalar.
    pub old: String,
    /// Rendered new scalar.
    pub new: String,
    /// When set, rendered verbatim instead of old/new.
    pub inline: String,
}

impl Leaf {
    /// The right-hand side of the `=`.
    pub fn value(&self) -> String {
        if !self.inline.is_empty() {
            return self.inline.clone();
        }
        match self.op {
            LeafOp::Add => self.new.clone(),
            LeafOp::Remove => self.old.clone(),
            LeafOp::Change => format!("{} → {}", self.old, self.new),
        }
    }
}

/// A render variant level, most to least detail.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Level {
    /// Scalar, sensitive or known-after-apply.
    Inline,
    /// Changed paths only.
    Structural,
    /// Unified line diff.
    LineDiff,
    /// Magnitude only.
    Summary,
    /// Omitted.
    Hidden,
}

/// One possible rendering of an attribute diff.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Variant {
    /// Level.
    pub level: Level,
    /// Markdown lines inside the diff block.
    pub content: String,
}

impl Variant {
    /// A variant with its content.
    pub fn new(level: Level, content: String) -> Self {
        Variant { level, content }
    }

    /// Content length in bytes, which fit uses to pick the largest first.
    pub fn bytes(&self) -> usize {
        self.content.len()
    }
}

/// One top-level attribute of a resource change: inline leaves, or a foldable block with the
/// variant ladder fit degrades.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Field {
    /// Attribute name.
    pub name: String,
    /// "json" or "yaml" for structured blocks, else empty.
    pub kind: String,
    /// Inline rows; empty for a block.
    pub leaves: Vec<Leaf>,
    /// Block ladder; empty for leaves.
    pub variants: Vec<Variant>,
    /// Chosen variant (blocks only); fit changes it.
    pub selected: usize,
    /// The attribute only changed sensitivity.
    pub sensitivity_only: bool,
}

impl Field {
    /// Whether this field renders as a block.
    pub fn is_block(&self) -> bool {
        !self.variants.is_empty()
    }

    /// The selected variant (blocks only).
    pub fn sel(&self) -> &Variant {
        &self.variants[self.selected]
    }

    /// Whether the selected variant is the least-detail one.
    pub fn at_last(&self) -> bool {
        self.selected + 1 >= self.variants.len()
    }
}

/// A labelled report-header link.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Link {
    /// Label.
    pub label: String,
    /// URL.
    pub url: String,
}

/// One resource change within a stack.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    /// Address.
    pub address: String,
    /// Resource type.
    pub type_: String,
    /// Action bucket.
    pub action: Action,
    /// Attribute fields.
    pub fields: Vec<Field>,
    /// Link.
    pub url: String,
    /// Moved (in-stack or cross-state).
    pub moved: bool,
    /// "in", "out", "in-update" or empty.
    pub move_direction: String,
    /// Old address when moved in-stack.
    pub previous_address: String,
    /// Imported.
    pub imported: bool,
    /// Import id.
    pub import_id: String,
    /// Only sensitivity changed.
    pub sensitivity_only: bool,
}

/// One stack's parsed, classified plan.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Stack {
    /// Name.
    pub name: String,
    /// Link.
    pub url: String,
    /// Tallies.
    pub counts: Counts,
    /// Matched categories in rule order; empty renders the default.
    pub categories: Vec<Category>,
    /// Changes.
    pub changes: Vec<Change>,
}

/// The terminal-cascade level fit chooses.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum Mode {
    /// Table and per-stack details.
    #[default]
    Full,
    /// Table and notice, no details.
    SummaryOnly,
    /// One-line aggregate and notice.
    Minimal,
}

/// The complete report.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Report {
    /// Title.
    pub title: String,
    /// HTML-comment marker.
    pub marker: String,
    /// Whether to show the Categories column.
    pub classified: bool,
    /// Badge for a stack that matched no category.
    pub default: Category,
    /// Stacks.
    pub stacks: Vec<Stack>,
    /// Mode.
    pub mode: Mode,
    /// Cascade notice.
    pub notice: String,
    /// Render `<details open>`.
    pub details_open: bool,
    /// Header links.
    pub header_links: Vec<Link>,
}

#[cfg(test)]
mod tests;
