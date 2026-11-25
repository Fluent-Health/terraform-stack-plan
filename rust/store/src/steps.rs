//! One decide, fold, persist and project round per aggregate (Go: `Shell.Handle`'s
//! inner step, `Shell.HandleExec` and `Shell.handleClaim`), and rebuilding the
//! projections from the log. Running the actions, and the fixpoint loop over their
//! feedback signals, is the serve layer's; it holds the key's [`KeyedLocks`]
//! lock around the round and the actions.
//!
//! [`KeyedLocks`]: crate::KeyedLocks

use std::collections::HashSet;

use tfstackplan_gate::claims::{self, Claims};
use tfstackplan_gate::execution::{self, Execution};
use tfstackplan_gate::reconcile::{self, Action, ChangeSet, Event, Gate, Signal};

use crate::claims::{project_claims_rows, replace_claims_in};
use crate::execution::{append_phase_history, project_execution};
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

    /// Rewrites (pr, env)'s `gate_targets` rows and stack overlay from its gate,
    /// and marks failed every execution whose start failed and that the runner
    /// never completed. (A round marks only the start failures of its own batch;
    /// over the whole log, a later `RunCompleted` is what un-fails a run.)
    pub fn rebuild_gate(&self, pr: i64, env: &str) -> Result<()> {
        let (stored, _) = self.load_events(&exec_stream_id(pr, env))?;
        let events = stored
            .iter()
            .map(|se| reconcile::unmarshal_event(&se.kind, &se.data))
            .collect::<Result<Vec<_>, _>>()?;
        let cs = events.iter().fold(
            ChangeSet {
                pr,
                environment: env.to_string(),
                ..ChangeSet::default()
            },
            reconcile::evolve,
        );
        let completed: HashSet<&str> = events
            .iter()
            .filter_map(|e| match e {
                Event::RunCompleted(rc) => Some(rc.execution_id.as_str()),
                _ => None,
            })
            .collect();
        let failed: Vec<Event> = events
            .iter()
            .filter(|e| {
                matches!(e, Event::RunStartFailed(rf) if !completed.contains(rf.execution_id.as_str()))
            })
            .cloned()
            .collect();
        self.write(|tx| project_gate(tx, &cs, &failed))
    }

    /// Rewrites an execution's row, stacks and edges from its stream, and its
    /// phase history from every `PhaseChanged` in the log.
    pub fn rebuild_execution(&self, exec_id: &str) -> Result<()> {
        let stream = run_stream_id(exec_id);
        let (stored, _) = self.load_events(&stream)?;
        let events = stored
            .iter()
            .map(|se| execution::unmarshal_event(&se.kind, &se.data))
            .collect::<Result<Vec<_>, _>>()?;
        let state = events
            .iter()
            .fold(execution::State::default(), execution::evolve);
        self.write(|tx| {
            tx.execute(
                "DELETE FROM execution_phases WHERE execution_id = ?1",
                [&state.id],
            )?;
            project_execution(tx, &state, &[])?;
            for e in &events {
                if let execution::Event::PhaseChanged(pc) = e {
                    append_phase_history(tx, &state.id, pc.phase.as_str(), &pc.label, pc.pct)?;
                }
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
