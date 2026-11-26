//! The `apply_claims` projection of the claim ledger (Go:
//! `internal/store/claims.go` and `internal/server/claimshell.go`). The
//! `env:<env>` stream is the truth; this table is a cross-environment index for
//! the expiry sweep and the live list.

use std::collections::HashSet;

use jiff::Timestamp;
use rusqlite::{Transaction, params};
use tfstackplan_gate::claims::ClaimSet;
use tfstackplan_gate::events::Claim;

use crate::{Result, Store, time};

/// One stack's claim in the projection.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ClaimRow {
    pub owner_pr: i64,
    pub expires_at: Timestamp,
}

/// The projection rows of a folded ledger.
pub fn project_claims_rows(cs: &ClaimSet) -> Vec<(String, ClaimRow)> {
    cs.iter()
        .map(|(stack, c)| {
            (
                stack.clone(),
                ClaimRow {
                    owner_pr: c.pr,
                    expires_at: c.expires_at,
                },
            )
        })
        .collect()
}

/// Rewrites `env`'s rows to exactly `rows`: upserts each and deletes the rest.
pub(crate) fn replace_claims_in(
    tx: &Transaction<'_>,
    env: &str,
    rows: &[(String, ClaimRow)],
) -> Result<()> {
    let mut keep = HashSet::new();
    for (stack, c) in rows {
        keep.insert(stack.as_str());
        tx.execute(
            "INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at)
             VALUES (?1, ?2, ?3, NULL, ?4)
             ON CONFLICT(environment, stack_path) DO UPDATE SET
               owner_pr=excluded.owner_pr, expires_at=excluded.expires_at",
            params![env, stack, c.owner_pr, time::to_sql(c.expires_at)],
        )?;
    }
    let existing = {
        let mut stmt = tx.prepare("SELECT stack_path FROM apply_claims WHERE environment = ?1")?;
        stmt.query_map([env], |r| r.get::<_, String>(0))?
            .collect::<rusqlite::Result<Vec<_>>>()?
    };
    for stack in existing.iter().filter(|s| !keep.contains(s.as_str())) {
        tx.execute(
            "DELETE FROM apply_claims WHERE environment = ?1 AND stack_path = ?2",
            params![env, stack],
        )?;
    }
    Ok(())
}

impl Store {
    /// Rewrites `env`'s `apply_claims` rows to exactly `rows`, in one transaction.
    pub fn replace_claims(&self, env: &str, rows: &[(String, ClaimRow)]) -> Result<()> {
        self.write(|tx| replace_claims_in(tx, env, rows))
    }

    /// Links a PR's claims to the apply execution that now owns them.
    pub fn associate_claim_execution(&self, env: &str, pr: i64, exec_id: &str) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "UPDATE apply_claims SET execution_id = ?1 WHERE environment = ?2 AND owner_pr = ?3",
                params![exec_id, env, pr],
            )?;
            Ok(())
        })
    }

    /// Deletes every claim expired at `now` and returns the environments that lost
    /// one, so their held checks can be re-evaluated.
    pub fn sweep_expired_claims(&self, now: Timestamp) -> Result<Vec<String>> {
        let now = time::to_sql(now);
        self.write(|tx| {
            let envs = {
                let mut stmt = tx.prepare(
                    "SELECT DISTINCT environment FROM apply_claims WHERE expires_at <= ?1",
                )?;
                stmt.query_map([&now], |r| r.get::<_, String>(0))?
                    .collect::<rusqlite::Result<Vec<_>>>()?
            };
            tx.execute("DELETE FROM apply_claims WHERE expires_at <= ?1", [&now])?;
            Ok(envs)
        })
    }

    /// Every claim of `env`, expired or not, by stack path.
    pub fn list_claims(&self, env: &str) -> Result<Vec<Claim>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT environment, stack_path, owner_pr, expires_at FROM apply_claims
             WHERE environment = ?1 ORDER BY stack_path",
        )?;
        let rows = stmt
            .query_map([env], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, i64>(2)?,
                    r.get::<_, String>(3)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        rows.into_iter()
            .map(|(environment, stack_path, owner_pr, expires_at)| {
                Ok(Claim {
                    environment,
                    stack_path,
                    owner_pr,
                    expires_at: time::from_sql(&expires_at)?,
                })
            })
            .collect()
    }
}

#[cfg(test)]
mod tests;
