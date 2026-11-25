// Ported from internal/store/eventstore_test.go, busy_test.go and
// internal/eventsourcing/host_test.go.
use std::sync::Barrier;
use std::thread;

use serde::{Deserialize, Serialize};
use tfstackplan_gate::eventsourcing::{CodecError, Decider};

use super::*;
use crate::db::tests::temp_store;

fn ev(kind: &str, data: &str) -> StoredEvent {
    StoredEvent {
        kind: kind.into(),
        data: data.as_bytes().to_vec(),
    }
}

#[test]
fn event_store_append_and_load() {
    let (_dir, s) = temp_store();
    s.append_events("exec:1:nonprod", 0, &[ev("A", "a"), ev("B", "b")])
        .unwrap();
    let (got, version) = s.load_events("exec:1:nonprod").unwrap();
    assert_eq!(version, 2);
    assert_eq!(got, vec![ev("A", "a"), ev("B", "b")]);
}

#[test]
fn event_store_contiguous_append() {
    let (_dir, s) = temp_store();
    s.append_events("s", 0, &[ev("A", "1")]).unwrap();
    s.append_events("s", 1, &[ev("B", "2")]).unwrap();
    let (got, version) = s.load_events("s").unwrap();
    assert_eq!((version, got.len()), (2, 2));
}

#[test]
fn event_store_stale_version_conflict_writes_nothing() {
    let (_dir, s) = temp_store();
    s.append_events("s", 0, &[ev("A", "1")]).unwrap();
    let err = s.append_events("s", 0, &[ev("B", "2")]).unwrap_err();
    assert!(matches!(err, Error::ConcurrencyConflict), "{err}");
    assert_eq!(s.load_events("s").unwrap().1, 1);
}

#[test]
fn event_store_empty_stream() {
    let (_dir, s) = temp_store();
    let (got, version) = s.load_events("nope").unwrap();
    assert_eq!((version, got.len()), (0, 0));
}

#[test]
fn event_store_concurrent_append_one_wins() {
    let (_dir, s) = temp_store();
    let barrier = Barrier::new(2);
    let results: Vec<Result<()>> = thread::scope(|scope| {
        let handles: Vec<_> = (0..2)
            .map(|_| {
                scope.spawn(|| {
                    barrier.wait();
                    s.append_events("s", 0, &[ev("A", "x")])
                })
            })
            .collect();
        handles.into_iter().map(|h| h.join().unwrap()).collect()
    });
    let ok = results.iter().filter(|r| r.is_ok()).count();
    let conflicts = results
        .iter()
        .filter(|r| matches!(r, Err(Error::ConcurrencyConflict)))
        .count();
    assert_eq!((ok, conflicts), (1, 1), "{results:?}");
    assert_eq!(s.load_events("s").unwrap().1, 1);
}

#[test]
fn event_store_append_empty_is_noop() {
    let (_dir, s) = temp_store();
    s.append_events("s", 0, &[]).unwrap();
    assert_eq!(s.load_events("s").unwrap().1, 0);
}

#[test]
fn event_store_data_byte_fidelity() {
    let (_dir, s) = temp_store();
    let blob = vec![0x00, 0x01, 0xff, 0xfe, b'{', b'}'];
    s.append_events(
        "s",
        0,
        &[StoredEvent {
            kind: "X".into(),
            data: blob.clone(),
        }],
    )
    .unwrap();
    assert_eq!(s.load_events("s").unwrap().0[0].data, blob);
}

#[test]
fn snapshot_save_and_load() {
    let (_dir, s) = temp_store();
    s.save_snapshot("s", 5, b"state-v5").unwrap();
    assert_eq!(
        s.load_snapshot("s").unwrap(),
        Some((b"state-v5".to_vec(), 5))
    );
}

#[test]
fn snapshot_overwrite_keeps_latest() {
    let (_dir, s) = temp_store();
    s.save_snapshot("s", 5, b"old").unwrap();
    s.save_snapshot("s", 9, b"new").unwrap();
    assert_eq!(s.load_snapshot("s").unwrap(), Some((b"new".to_vec(), 9)));
}

#[test]
fn snapshot_load_missing() {
    let (_dir, s) = temp_store();
    assert_eq!(s.load_snapshot("nope").unwrap(), None);
}

#[test]
fn snapshot_state_byte_fidelity() {
    let (_dir, s) = temp_store();
    let blob = [0x00, 0x7f, 0xff, b'}'];
    s.save_snapshot("s", 1, &blob).unwrap();
    assert_eq!(s.load_snapshot("s").unwrap().unwrap().0, blob);
}

#[test]
fn is_unique_violation_maps_pk_conflict() {
    let (_dir, s) = temp_store();
    let conn = s.conn();
    conn.execute(
        "INSERT INTO events (stream_id, version, type, data) VALUES ('s',1,'x','{}')",
        [],
    )
    .unwrap();
    let err = conn
        .execute(
            "INSERT INTO events (stream_id, version, type, data) VALUES ('s',1,'y','{}')",
            [],
        )
        .unwrap_err();
    assert!(is_unique_violation(&err), "{err}");
}

/// busy_test.go: appends while other connections write. A deferred transaction
/// that reads before it writes fails with SQLITE_BUSY at once when another
/// connection commits in between; BEGIN IMMEDIATE waits instead. As in Go, the
/// appenders share the store (and its in-process lock) and every writer has its
/// own connection.
#[test]
fn append_survives_concurrent_writers() {
    let (dir, s) = temp_store();
    let path = dir.path().join("test.db");
    const APPENDERS: usize = 4;
    const WRITERS: usize = 4;
    const ROUNDS: i64 = 50;
    let writers: Vec<Store> = (0..WRITERS).map(|_| Store::open(&path).unwrap()).collect();
    let errors: Vec<String> = thread::scope(|scope| {
        let mut handles = Vec::new();
        for a in 0..APPENDERS {
            let s = &s;
            handles.push(scope.spawn(move || {
                let stream = format!("run:exec-{a}");
                for v in 0..ROUNDS {
                    s.append_events(&stream, v, &[ev("tick", "{}")])
                        .map_err(|e| format!("append {stream} v{v}: {e}"))?;
                }
                Ok::<(), String>(())
            }));
        }
        // Snapshot writers on other connections stand in for the log pump and a
        // sibling handler's writes.
        for (w, store) in writers.iter().enumerate() {
            handles.push(scope.spawn(move || {
                for i in 0..ROUNDS {
                    store
                        .save_snapshot(&format!("noise-{w}"), i, b"{}")
                        .map_err(|e| format!("writer {w}: {e}"))?;
                }
                Ok(())
            }));
        }
        handles
            .into_iter()
            .filter_map(|h| h.join().unwrap().err())
            .collect()
    });
    assert!(errors.is_empty(), "{errors:?}");
    for a in 0..APPENDERS {
        assert_eq!(s.load_events(&format!("run:exec-{a}")).unwrap().1, ROUNDS);
    }
}

// --- the decider host (internal/eventsourcing/host_test.go) ---

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct Add {
    #[serde(rename = "N")]
    n: i64,
}

/// A counter: the state is the sum of the events.
struct Counter;

impl Decider for Counter {
    type State = i64;
    type Event = Add;

    fn initial() -> i64 {
        0
    }
    fn evolve(state: i64, e: &Add) -> i64 {
        state + e.n
    }
    fn marshal_event(e: &Add) -> Result<(&'static str, Vec<u8>), CodecError> {
        Ok(("add", serde_json::to_vec(e)?))
    }
    fn unmarshal_event(_tag: &str, data: &[u8]) -> Result<Add, CodecError> {
        Ok(serde_json::from_slice(data)?)
    }
    fn marshal_snapshot(s: &i64) -> Result<Vec<u8>, CodecError> {
        Ok(serde_json::to_vec(s)?)
    }
    fn unmarshal_snapshot(data: &[u8]) -> Result<i64, CodecError> {
        Ok(serde_json::from_slice(data)?)
    }
}

/// The counter, failing every event decode.
struct BadCounter;

impl Decider for BadCounter {
    type State = i64;
    type Event = Add;

    fn initial() -> i64 {
        0
    }
    fn evolve(state: i64, e: &Add) -> i64 {
        state + e.n
    }
    fn marshal_event(e: &Add) -> Result<(&'static str, Vec<u8>), CodecError> {
        Counter::marshal_event(e)
    }
    fn unmarshal_event(tag: &str, _data: &[u8]) -> Result<Add, CodecError> {
        Err(CodecError::UnknownTag {
            aggregate: "unmarshal boom",
            tag: tag.to_string(),
        })
    }
    fn marshal_snapshot(s: &i64) -> Result<Vec<u8>, CodecError> {
        Counter::marshal_snapshot(s)
    }
    fn unmarshal_snapshot(data: &[u8]) -> Result<i64, CodecError> {
        Counter::unmarshal_snapshot(data)
    }
}

#[test]
fn host_append_load_replay() {
    let (_dir, s) = temp_store();
    s.append::<Counter>("c", 0, &[Add { n: 2 }, Add { n: 3 }], &5)
        .unwrap();
    assert_eq!(s.load::<Counter>("c").unwrap(), (5, 2));
}

#[test]
fn host_empty_stream_is_initial() {
    let (_dir, s) = temp_store();
    assert_eq!(s.load::<Counter>("nope").unwrap(), (0, 0));
}

#[test]
fn host_stale_version_conflict() {
    let (_dir, s) = temp_store();
    s.append::<Counter>("c", 0, &[Add { n: 1 }], &1).unwrap();
    let err = s
        .append::<Counter>("c", 0, &[Add { n: 1 }], &2)
        .unwrap_err();
    assert!(matches!(err, Error::ConcurrencyConflict), "{err}");
}

#[test]
fn host_snapshot_plus_tail() {
    let (_dir, s) = temp_store();
    s.append::<Counter>("c", 0, &[Add { n: 10 }], &10).unwrap(); // snapshot 10 at v1
    s.append::<Counter>("c", 1, &[Add { n: 5 }], &15).unwrap(); // snapshot 15 at v2
    assert_eq!(s.load::<Counter>("c").unwrap(), (15, 2));
    // A tail newer than the snapshot folds on top of it.
    s.append_events("c", 2, &[ev("add", r#"{"N":1}"#)]).unwrap();
    assert_eq!(s.load::<Counter>("c").unwrap(), (16, 3));
    // A replay from the first event agrees with snapshot plus tail.
    assert_eq!(s.replay::<Counter>("c").unwrap(), (16, 3));
}

#[test]
fn host_load_unmarshal_failure() {
    let (_dir, s) = temp_store();
    // Appended without a snapshot, so the load must decode the events.
    s.append_events(
        "c-bad",
        0,
        &[ev("add", r#"{"N":2}"#), ev("add", r#"{"N":3}"#)],
    )
    .unwrap();
    let err = s.load::<BadCounter>("c-bad").unwrap_err();
    assert!(err.to_string().contains("unmarshal boom"), "{err}");
}
