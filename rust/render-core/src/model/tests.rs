use super::*;

#[test]
fn counts_total() {
    let c = Counts {
        add: 1,
        change: 2,
        destroy: 3,
        replace: 4,
        ..Counts::default()
    };
    assert_eq!(c.total(), 10);
}

#[test]
fn counts_any_change() {
    assert!(!Counts::default().any_change());
    assert!(
        Counts {
            change: 1,
            ..Counts::default()
        }
        .any_change()
    );
}

fn leaf(op: LeafOp, path: &str, old: &str, new: &str, inline: &str) -> Leaf {
    Leaf {
        op,
        path: path.to_string(),
        old: old.to_string(),
        new: new.to_string(),
        inline: inline.to_string(),
    }
}

#[test]
fn leaf_value() {
    let cases = [
        (
            leaf(LeafOp::Add, "team", "", "\"platform\"", ""),
            "\"platform\"",
        ),
        (leaf(LeafOp::Remove, "id", "\"x\"", "", ""), "\"x\""),
        (leaf(LeafOp::Change, "n", "7", "30", ""), "7 → 30"),
        (
            leaf(LeafOp::Change, "pw", "a", "b", "(sensitive value)"),
            "(sensitive value)",
        ),
    ];
    for (l, want) in cases {
        assert_eq!(l.value(), want);
    }
}

#[test]
fn field_is_block() {
    let leaf_field = Field {
        name: "labels".to_string(),
        leaves: vec![leaf(LeafOp::Add, "labels.team", "", "\"x\"", "")],
        ..Field::default()
    };
    assert!(!leaf_field.is_block());
    let block = Field {
        name: "data".to_string(),
        variants: vec![Variant::new(Level::LineDiff, "x".to_string())],
        ..Field::default()
    };
    assert!(block.is_block());
}

#[test]
fn action_mutates() {
    for (a, want) in [
        (Action::Add, true),
        (Action::Change, true),
        (Action::Destroy, true),
        (Action::Replace, true),
        (Action::Forget, false),
        (Action::Noop, false),
    ] {
        assert_eq!(a.mutates(), want, "{a:?}");
    }
}

#[test]
fn link_fields() {
    let r = Report {
        header_links: vec![Link {
            label: "PR #1".to_string(),
            url: "https://x/1".to_string(),
        }],
        stacks: vec![Stack {
            name: "s".to_string(),
            url: "https://x/s".to_string(),
            counts: Counts::default(),
            categories: Vec::new(),
            changes: Vec::new(),
        }],
        ..Report::default()
    };
    assert_eq!(r.header_links[0].url, "https://x/1");
    assert!(!r.stacks[0].url.is_empty());
}

#[test]
fn leaf_op_sym() {
    assert_eq!(LeafOp::Add.sym(), "+");
    assert_eq!(LeafOp::Remove.sym(), "-");
    assert_eq!(LeafOp::Change.sym(), "~");
}

#[test]
fn field_sel_and_at_last() {
    let mut f = Field {
        name: "data".to_string(),
        variants: vec![
            Variant::new(Level::Structural, "short".to_string()),
            Variant::new(Level::LineDiff, "long".to_string()),
        ],
        ..Field::default()
    };
    assert_eq!(f.sel().content, "short");
    assert!(!f.at_last());
    f.selected = 1;
    assert!(f.at_last());
}

#[test]
fn sensitivity_only() {
    let c = Counts {
        sensitivity_only: 3,
        ..Counts::default()
    };
    assert_eq!(c.sensitivity_only, 3);
    let f = Field {
        sensitivity_only: true,
        ..Field::default()
    };
    assert!(f.sensitivity_only);
}

// alias_test.go: model.Counts/Class are the domain types; here they are the same types.
#[test]
fn model_types_are_domain_aliases() {
    let cl = Category {
        name: "iam".to_string(),
        icon: "🔐".to_string(),
        attributes: None,
    };
    assert_eq!(cl.label(), "🔐 iam");
    assert!(
        Counts {
            change: 1,
            ..Counts::default()
        }
        .any_change()
    );
}

// domain_test.go

#[test]
fn counts_total_and_any_change() {
    let c = Counts {
        add: 1,
        change: 2,
        replace: 1,
        ..Counts::default()
    };
    assert_eq!(c.total(), 4);
    assert!(c.any_change());
    assert!(
        Counts {
            moved: 1,
            ..Counts::default()
        }
        .any_change()
    );
}

#[test]
fn category_label() {
    let mut c = Category {
        name: "iam".to_string(),
        icon: "🔐".to_string(),
        attributes: None,
    };
    assert_eq!(c.label(), "🔐 iam");
    c.icon.clear();
    c.name = "safe".to_string();
    assert_eq!(c.label(), "safe");
}
