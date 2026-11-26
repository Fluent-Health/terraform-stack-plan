//! One decide, fold, persist and project round per aggregate (Go: `Shell.Handle`'s
//! inner step, `Shell.HandleExec` and `Shell.handleClaim`), and rebuilding the
//! projections from the log. Running the actions, and the fixpoint loop over their
//! feedback signals, is the serve layer's; it holds the key's [`KeyedLocks`]
//! lock around the round and the actions.
//!
//! [`KeyedLocks`]: crate::KeyedLocks

use tfstackplan_gate::claims::{self, Claims};
use tfstackplan_gate::execution::{self, Execution};
use tfstackplan_gate::reconcile::{self, Action, ChangeSet, Event, Gate, Signal};

use crate::claims::{project_claims_rows, replace_claims_in};
use crate::execution::project_execution;
use crate::gate::project_gate;
use crate::{Error, Result, Store};

/// The gate stream of one PR in one environment.
pub fn exec_stream_id(pr: i64, env: &str) -> String {
    format!("exec:{pr}:{env}")
}

/// The lifecycle stream of one execution.
pub fn run_stream_id(exec_id: &str) -> String {
    format!("run:{exec_id}")
}

/// The claim-ledger stream of one environment.
pub fn env_stream_id(env: &str) -> String {
    format!("env:{env}")
}

/// What one gate round decided.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GateStep {
    /// The change set after folding `events`.
    pub state: ChangeSet,
    pub events: Vec<Event>,
    /// What the shell must do, in order.
    pub actions: Vec<Action>,
}

/// How often a claim command reloads after losing a version race.
const CLAIM_ATTEMPTS: usize = 8;

impl Store {
    /// The folded gate of (pr, env) and its stream version.
    pub fn load_gate(&self, pr: i64, env: &str) -> Result<(ChangeSet, i64)> {
        let (mut cs, version) = self.load::<Gate>(&exec_stream_id(pr, env))?;
        cs.pr = pr;
        cs.environment = env.to_string();
        Ok((cs, version))
    }

    /// One gate round for (pr, env): replay, decide, fold, append the events and a
    /// snapshot, rebuild the projections, and return the actions. The caller holds
    /// the (pr, env) lock; without it a concurrent round on the same key fails
    /// with [`Error::ConcurrencyConflict`] rather than losing an event.
    pub fn step_gate(&self, pr: i64, env: &str, signal: &Signal) -> Result<GateStep> {
        let (prior, version) = self.load_gate(pr, env)?;
        let events = reconcile::decide(&prior, signal);
        let state = events.iter().fold(prior, reconcile::evolve);
        self.append::<Gate>(&exec_stream_id(pr, env), version, &events, &state)?;
        self.write(|tx| project_gate(tx, &state, &events))?;
        let actions = reconcile::react(&state, &events);
        Ok(GateStep {
            state,
            events,
            actions,
        })
    }

    /// One execution round: replay, decide, fold, append and project. The caller
    /// holds the execution's lock.
    pub fn step_execution(
        &self,
        exec_id: &str,
        signal: &execution::Signal,
    ) -> Result<execution::State> {
        let stream = run_stream_id(exec_id);
        let (state, version) = self.load::<Execution>(&stream)?;
        let events = execution::decide(&state, signal);
        if events.is_empty() {
            return Ok(state);
        }
        let state = events.iter().fold(state, execution::evolve);
        self.append::<Execution>(&stream, version, &events, &state)?;
        self.write(|tx| project_execution(tx, &state, &events))?;
        Ok(state)
    }

    /// Applies one claim command to `env`'s ledger and rewrites its projection,
    /// reloading when another writer advanced the stream in between. The caller
    /// holds the environment's lock.
    pub fn step_claims(&self, env: &str, command: &claims::Command) -> Result<claims::ClaimSet> {
        let stream = env_stream_id(env);
        for _ in 0..CLAIM_ATTEMPTS {
            let (state, version) = self.load::<Claims>(&stream)?;
            let events = claims::decide(&state, command);
            let state = events.iter().fold(state, claims::evolve);
            match self.append::<Claims>(&stream, version, &events, &state) {
                Err(Error::ConcurrencyConflict) => continue,
                Err(e) => return Err(e),
                Ok(()) => {}
            }
            self.replace_claims(env, &project_claims_rows(&state))?;
            return Ok(state);
        }
        Err(Error::ConcurrencyConflict)
    }

    /// The folded claim ledger of `env`.
    pub fn load_claims(&self, env: &str) -> Result<claims::ClaimSet> {
        Ok(self.load::<Claims>(&env_stream_id(env))?.0)
    }

    /// Rewrites `env`'s `apply_claims` rows from its ledger.
    pub fn rebuild_claims(&self, env: &str) -> Result<()> {
        let cs = self.load_claims(env)?;
        self.write(|tx| replace_claims_in(tx, env, &project_claims_rows(&cs)))
    }

    /// Rewrites (pr, env)'s projections by replaying its log one event at a time
    /// and projecting after each, as the rounds did. Some of what Go's rounds wrote
    /// is not in the final fold (an execution failed by a start failure, the last
    /// stack overlay of a released gate), so projecting only the end state would
    /// lose it.
    pub fn rebuild_gate(&self, pr: i64, env: &str) -> Result<()> {
        let (stored, _) = self.load_events(&exec_stream_id(pr, env))?;
        let events = stored
            .iter()
            .map(|se| reconcile::unmarshal_event(&se.kind, &se.data))
            .collect::<Result<Vec<_>, _>>()?;
        self.write(|tx| {
            let mut cs = ChangeSet {
                pr,
                environment: env.to_string(),
                ..ChangeSet::default()
            };
            project_gate(tx, &cs, &[])?;
            for e in events {
                cs = reconcile::evolve(cs, &e);
                project_gate(tx, &cs, std::slice::from_ref(&e))?;
            }
            Ok(())
        })
    }

    /// Rewrites an execution's row, stacks, edges and phase history by replaying
    /// its log one event at a time and projecting after each, as the rounds did:
    /// a row keeps a progress label that a later phase without one leaves alone.
    pub fn rebuild_execution(&self, exec_id: &str) -> Result<()> {
        let (stored, _) = self.load_events(&run_stream_id(exec_id))?;
        let events = stored
            .iter()
            .map(|se| execution::unmarshal_event(&se.kind, &se.data))
            .collect::<Result<Vec<_>, _>>()?;
        self.write(|tx| {
            tx.execute(
                "DELETE FROM execution_phases WHERE execution_id = ?1",
                [exec_id],
            )?;
            let mut state = execution::State::default();
            for e in events {
                state = execution::evolve(state, &e);
                project_execution(tx, &state, std::slice::from_ref(&e))?;
            }
            Ok(())
        })
    }

    /// Rebuilds every projection from the log: executions first, then the gates
    /// (whose overlay writes their stacks), then the claim ledgers.
    pub fn rebuild_all(&self) -> Result<()> {
        for stream in self.stream_ids("run:")? {
            if let Some(id) = stream.strip_prefix("run:") {
                self.rebuild_execution(id)?;
            }
        }
        for stream in self.stream_ids("exec:")? {
            if let Some((pr, env)) = stream
                .strip_prefix("exec:")
                .and_then(|rest| rest.split_once(':'))
                && let Ok(pr) = pr.parse()
            {
                self.rebuild_gate(pr, env)?;
            }
        }
        for stream in self.stream_ids("env:")? {
            if let Some(env) = stream.strip_prefix("env:") {
                self.rebuild_claims(env)?;
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests;
