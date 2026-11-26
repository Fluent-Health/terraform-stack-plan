use super::{
    Event, Failed, PhaseChanged, Signal, StackStatusChanged, StacksAnnotated, Started, State,
    Succeeded, Superseded,
};

/// The facts a runner report produces. Pure: no I/O, no clock.
pub fn decide(s: &State, sig: &Signal) -> Vec<Event> {
    match sig {
        Signal::ReportInit(v) => vec![
            Started {
                exec: v.exec.clone(),
            }
            .into(),
        ],
        Signal::ReportPhase(v) => vec![
            PhaseChanged {
                phase: v.phase.clone(),
                label: v.label.clone(),
                pct: v.pct,
                id: v.id.clone(),
                pr: v.pr,
                repo: v.repo.clone(),
                sha: v.sha.clone(),
                environment: v.environment.clone(),
                context: v.context.clone(),
                log_url: v.log_url.clone(),
            }
            .into(),
        ],
        Signal::ReportTick(v) => vec![
            StackStatusChanged {
                stack: v.stack.clone(),
                status: v.status,
                detail: v.detail.clone(),
            }
            .into(),
        ],
        Signal::ReportFail(_) => vec![Failed {}.into()],
        Signal::ReportSucceed(_) => vec![Succeeded {}.into()],
        Signal::ReportAnnotate(v) => vec![
            StacksAnnotated {
                projects: v.projects.clone(),
                categories: v.categories.clone(),
                counts: v.counts.clone(),
                moving: v.moving.clone(),
            }
            .into(),
        ],
        Signal::ReportSupersede(v) => {
            if s.id.is_empty() {
                // No materialized execution to mark superseded.
                return vec![];
            }
            vec![Superseded { by: v.by.clone() }.into()]
        }
    }
}

#[cfg(test)]
mod tests;
