//! The append-only event log and snapshot cache, and the generic decider host over
//! them (Go: `internal/store/eventstore.go` and `internal/eventsourcing`).

use rusqlite::types::ValueRef;
use rusqlite::{ErrorCode, OptionalExtension, Row, params};
use tfstackplan_gate::eventsourcing::Decider;

use crate::{Error, Result, Store};

/// One stored fact: an event type tag and its opaque payload (JSON in practice).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StoredEvent {
    pub kind: String,
    pub data: Vec<u8>,
}

impl Store {
    /// Appends `evs` as the versions after `expected_version` (the caller's last
    /// seen version; 0 for an empty stream), all or nothing. Returns
    /// [`Error::ConcurrencyConflict`] when the stream is not at `expected_version`.
    pub fn append_events(
        &self,
        stream_id: &str,
        expected_version: i64,
        evs: &[StoredEvent],
    ) -> Result<()> {
        if evs.is_empty() {
            return Ok(());
        }
        self.write(|tx| append_in(tx, stream_id, expected_version, evs))
    }

    /// The stream's events in version order and its current version (0 for an
    /// unknown stream).
    pub fn load_events(&self, stream_id: &str) -> Result<(Vec<StoredEvent>, i64)> {
        self.load_events_after(stream_id, 0)
    }

    fn load_events_after(&self, stream_id: &str, after: i64) -> Result<(Vec<StoredEvent>, i64)> {
        let conn = self.conn();
        let mut stmt = conn.prepare_cached(
            "SELECT version, type, data FROM events WHERE stream_id = ?1 AND version > ?2 ORDER BY version",
        )?;
        let mut rows = stmt.query(params![stream_id, after])?;
        let mut out = Vec::new();
        let mut version = after;
        while let Some(row) = rows.next()? {
            version = row.get(0)?;
            out.push(StoredEvent {
                kind: row.get(1)?,
                data: bytes(row, 2)?,
            });
        }
        if out.is_empty() {
            // `after` came from a snapshot; the stream's version is still the log's.
            version = conn.query_row(
                "SELECT COALESCE(MAX(version), 0) FROM events WHERE stream_id = ?1",
                [stream_id],
                |r| r.get(0),
            )?;
        }
        Ok((out, version))
    }

    /// Upserts the stream's snapshot at `version`, last write wins. The log is the
    /// authority, so a stale snapshot only means replaying a longer tail.
    pub fn save_snapshot(&self, stream_id: &str, version: i64, state: &[u8]) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "INSERT INTO snapshots (stream_id, version, state) VALUES (?1, ?2, ?3)
             ON CONFLICT(stream_id) DO UPDATE SET
               version = excluded.version, state = excluded.state,
               updated_at = CURRENT_TIMESTAMP",
                params![stream_id, version, state],
            )?;
            Ok(())
        })
    }

    /// The stream's snapshot and the version it reflects, if any.
    pub fn load_snapshot(&self, stream_id: &str) -> Result<Option<(Vec<u8>, i64)>> {
        Ok(self
            .conn()
            .query_row(
                "SELECT state, version FROM snapshots WHERE stream_id = ?1",
                [stream_id],
                |r| Ok((bytes(r, 0)?, r.get(1)?)),
            )
            .optional()?)
    }

    /// A stream's state: its snapshot folded forward over the newer events, and the
    /// stream's version (the initial state at version 0 for an empty stream).
    pub fn load<D: Decider>(&self, stream_id: &str) -> Result<(D::State, i64)> {
        let (state, after) = match self.load_snapshot(stream_id)? {
            Some((snap, version)) => (D::unmarshal_snapshot(&snap)?, version),
            None => (D::initial(), 0),
        };
        let (tail, version) = self.load_events_after(stream_id, after)?;
        let state = fold::<D>(state, &tail)?;
        Ok((state, version))
    }

    /// A stream's state folded from its first event, ignoring the snapshot.
    pub fn replay<D: Decider>(&self, stream_id: &str) -> Result<(D::State, i64)> {
        let (evs, version) = self.load_events(stream_id)?;
        Ok((fold::<D>(D::initial(), &evs)?, version))
    }

    /// Encodes `evs`, appends them at `expected_version`, then snapshots
    /// `new_state` at the resulting version. Nothing happens for no events.
    pub fn append<D: Decider>(
        &self,
        stream_id: &str,
        expected_version: i64,
        evs: &[D::Event],
        new_state: &D::State,
    ) -> Result<()> {
        if evs.is_empty() {
            return Ok(());
        }
        let stored = evs
            .iter()
            .map(|e| {
                let (kind, data) = D::marshal_event(e)?;
                Ok(StoredEvent {
                    kind: kind.to_string(),
                    data,
                })
            })
            .collect::<Result<Vec<_>>>()?;
        self.append_events(stream_id, expected_version, &stored)?;
        let snap = D::marshal_snapshot(new_state)?;
        self.save_snapshot(stream_id, expected_version + len(evs), &snap)
    }

    /// Every stream id with the given prefix, in order.
    pub fn stream_ids(&self, prefix: &str) -> Result<Vec<String>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT DISTINCT stream_id FROM events WHERE substr(stream_id, 1, length(?1)) = ?1 ORDER BY stream_id",
        )?;
        let ids = stmt
            .query_map([prefix], |r| r.get(0))?
            .collect::<rusqlite::Result<Vec<String>>>()?;
        Ok(ids)
    }
}

fn append_in(
    tx: &rusqlite::Transaction<'_>,
    stream_id: &str,
    expected_version: i64,
    evs: &[StoredEvent],
) -> Result<()> {
    let current: i64 = tx.query_row(
        "SELECT COALESCE(MAX(version), 0) FROM events WHERE stream_id = ?1",
        [stream_id],
        |r| r.get(0),
    )?;
    if current != expected_version {
        return Err(Error::ConcurrencyConflict);
    }
    let mut stmt = tx.prepare_cached(
        "INSERT INTO events (stream_id, version, type, data) VALUES (?1, ?2, ?3, ?4)",
    )?;
    for (version, e) in (expected_version + 1..).zip(evs) {
        stmt.execute(params![stream_id, version, e.kind, e.data])
            .map_err(|err| {
                if is_unique_violation(&err) {
                    Error::ConcurrencyConflict
                } else {
                    err.into()
                }
            })?;
    }
    Ok(())
}

/// Whether `err` is a PRIMARY KEY or UNIQUE violation: the schema's backstop on
/// `(stream_id, version)` if two writers ever race past the version check.
pub(crate) fn is_unique_violation(err: &rusqlite::Error) -> bool {
    matches!(
        err.sqlite_error(),
        Some(e) if e.code == ErrorCode::ConstraintViolation
            && matches!(
                e.extended_code,
                rusqlite::ffi::SQLITE_CONSTRAINT_PRIMARYKEY | rusqlite::ffi::SQLITE_CONSTRAINT_UNIQUE
            )
    )
}

fn fold<D: Decider>(state: D::State, evs: &[StoredEvent]) -> Result<D::State> {
    evs.iter().try_fold(state, |state, se| {
        Ok(D::evolve(state, &D::unmarshal_event(&se.kind, &se.data)?))
    })
}

/// A slice length as a stream version.
fn len<T>(evs: &[T]) -> i64 {
    i64::try_from(evs.len()).unwrap_or(i64::MAX)
}

/// A BLOB or TEXT column as bytes: Go writes `[]byte` as a BLOB, and a TEXT value
/// written by hand reads the same.
pub(crate) fn bytes(row: &Row<'_>, idx: usize) -> rusqlite::Result<Vec<u8>> {
    match row.get_ref(idx)? {
        ValueRef::Blob(b) | ValueRef::Text(b) => Ok(b.to_vec()),
        ValueRef::Null => Ok(Vec::new()),
        other => Err(rusqlite::Error::InvalidColumnType(
            idx,
            String::new(),
            other.data_type(),
        )),
    }
}

#[cfg(test)]
mod tests;
