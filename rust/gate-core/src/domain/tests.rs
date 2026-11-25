use std::collections::BTreeMap;

use super::*;

#[test]
fn counts_total_and_any_change() {
    let c = Counts {
        add: 1,
        change: 2,
        destroy: 0,
        replace: 1,
        ..Counts::default()
    };
    assert_eq!(c.total(), 4);
    assert!(c.any_change());
    assert!(!Counts::default().any_change());
    assert!(
        Counts {
            moved: 1,
            ..Counts::default()
        }
        .any_change()
    );
}

#[test]
fn counts_json_omits_zero() {
    let b = serde_json::to_string(&Counts {
        add: 2,
        ..Counts::default()
    })
    .unwrap();
    assert_eq!(b, r#"{"add":2}"#);
}

#[test]
fn category_label() {
    let iam = Category {
        name: "iam".into(),
        icon: "🔐".into(),
        ..Category::default()
    };
    assert_eq!(iam.label(), "🔐 iam");
    let safe = Category {
        name: "safe".into(),
        ..Category::default()
    };
    assert_eq!(safe.label(), "safe");
}

#[test]
fn category_json() {
    let c = Category {
        name: "iam".into(),
        icon: "🔐".into(),
        attributes: BTreeMap::from([("project".to_string(), vec!["p1".to_string()])]),
    };
    assert_eq!(
        serde_json::to_string(&c).unwrap(),
        r#"{"name":"iam","icon":"🔐","attributes":{"project":["p1"]}}"#
    );
    let bare = Category {
        name: "safe".into(),
        ..Category::default()
    };
    assert_eq!(serde_json::to_string(&bare).unwrap(), r#"{"name":"safe"}"#);
}
