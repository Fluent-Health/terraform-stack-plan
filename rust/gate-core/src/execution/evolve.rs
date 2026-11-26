use std::collections::{HashMap, HashSet};

use crate::events::Status;

use super::{Event, Stack, State};

/// Folds one event into the execution. Total, pure and deterministic.
pub fn evolve(mut s: State, e: &Event) -> State {
    match e {
        Event::Started(ev) => {
            // A fresh Init replaces the whole state except per-stack runner progress,
            // which never regresses: a stack seen before keeps its status, detail,
            // categories and counts, and only takes the new project (as the old
            // stack upsert did). Stacks missing from the new Init carry forward, in
            // their prior order.
            let prior: HashMap<&str, &Stack> =
                s.stacks.iter().map(|st| (st.path.as_str(), st)).collect();
            let seen: HashSet<&str> = ev.exec.stacks.iter().map(|st| st.path.as_str()).collect();
            let mut merged: Vec<Stack> = ev
                .exec
                .stacks
                .iter()
                .map(|st| match prior.get(st.path.as_str()) {
                    Some(old) => {
                        let mut old = (*old).clone();
                        if !st.project.is_empty() {
                            old.project = st.project.clone();
                        }
                        old
                    }
                    None => st.clone(),
                })
                .collect();
            merged.extend(
                s.stacks
                    .iter()
                    .filter(|st| !seen.contains(st.path.as_str()))
                    .cloned(),
            );
            State {
                stacks: merged,
                ..ev.exec.clone()
            }
        }
        Event::PhaseChanged(ev) => {
            s.phase = ev.phase.clone();
            s.progress_label = ev.label.clone();
            s.progress_pct = ev.pct;
            // Identity is set only where empty, so a phase before init materializes
            // the row and a later bare phase bump does not clobber it.
            fill(&mut s.id, &ev.id);
            if s.pr == 0 {
                s.pr = ev.pr;
            }
            fill(&mut s.repo, &ev.repo);
            fill(&mut s.sha, &ev.sha);
            fill(&mut s.environment, &ev.environment);
            fill(&mut s.context, &ev.context);
            fill(&mut s.log_url, &ev.log_url);
            s
        }
        Event::StackStatusChanged(ev) => {
            for st in s.stacks.iter_mut().filter(|st| st.path == ev.stack) {
                st.run_status = ev.status;
                st.detail = ev.detail.clone();
            }
            s
        }
        Event::Failed(_) => {
            s.status = "failure".into();
            // A stack still mid-run at a failed finalize did not fail itself:
            // terramate aborted it.
            for st in &mut s.stacks {
                if matches!(
                    st.run_status,
                    Status::Pending
                        | Status::Running
                        | Status::Initializing
                        | Status::Initialized
                        | Status::Moving
                ) {
                    st.run_status = Status::Aborted;
                }
            }
            s
        }
        Event::Succeeded(_) => {
            s.status = "success".into();
            s
        }
        Event::StacksAnnotated(ev) => {
            let moving: HashSet<&str> = ev.moving.iter().map(String::as_str).collect();
            for st in &mut s.stacks {
                if let Some(proj) = ev.projects.get(&st.path) {
                    st.project = proj.clone();
                }
                if let Some(cats) = ev.categories.get(&st.path) {
                    st.categories = cats.clone();
                }
                if let Some(c) = ev.counts.get(&st.path) {
                    st.counts = Some(*c);
                }
                // Moving overlays non-terminal stacks only.
                if moving.contains(st.path.as_str())
                    && !matches!(st.run_status, Status::Failed | Status::Aborted)
                {
                    st.run_status = Status::Moving;
                }
            }
            s
        }
        Event::Superseded(ev) => {
            s.superseded_by = ev.by.clone();
            s
        }
    }
}

fn fill(field: &mut String, value: &str) {
    if field.is_empty() {
        *field = value.to_string();
    }
}

#[cfg(test)]
mod tests;
