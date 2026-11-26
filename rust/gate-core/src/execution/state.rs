use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::events::{Category, Counts, Phase, Status};
use crate::json::{null_if_empty, nullable};
use crate::reconcile::wrap_variants;

/// The folded lifecycle of one execution. JSON field names are Go's.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct State {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "Repo")]
    pub repo: String,
    #[serde(rename = "SHA")]
    pub sha: String,
    #[serde(rename = "LogURL")]
    pub log_url: String,
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "Environment")]
    pub environment: String,
    #[serde(rename = "Context")]
    pub context: String,
    #[serde(rename = "Phase")]
    pub phase: Phase,
    #[serde(rename = "ProgressLabel")]
    pub progress_label: String,
    #[serde(rename = "ProgressPct")]
    pub progress_pct: Option<i64>,
    #[serde(rename = "Status")]
    pub status: String,
    #[serde(rename = "SupersededBy")]
    pub superseded_by: String,
    #[serde(rename = "Stacks", deserialize_with = "nullable")]
    pub stacks: Vec<Stack>,
    #[serde(
        rename = "Edges",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub edges: Vec<Edge>,
}

/// One node. `run_status` is only what the runner told; the gated/safe overlay is
/// the gate aggregate's projection.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Stack {
    #[serde(rename = "Path")]
    pub path: String,
    #[serde(rename = "Project")]
    pub project: String,
    #[serde(rename = "RunStatus")]
    pub run_status: Status,
    #[serde(rename = "Detail")]
    pub detail: String,
    #[serde(
        rename = "Categories",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub categories: Vec<Category>,
    #[serde(rename = "Counts")]
    pub counts: Option<Counts>,
}

/// A directed edge between two stacks.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Edge {
    #[serde(rename = "From")]
    pub from: String,
    #[serde(rename = "To")]
    pub to: String,
}

/// Everything that can drive an execution: what the CI runner reports.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Signal {
    ReportInit(ReportInit),
    ReportPhase(ReportPhase),
    ReportTick(ReportTick),
    ReportFail(ReportFail),
    ReportSucceed(ReportSucceed),
    ReportAnnotate(ReportAnnotate),
    ReportSupersede(ReportSupersede),
}

wrap_variants!(Signal: ReportInit, ReportPhase, ReportTick, ReportFail, ReportSucceed,
    ReportAnnotate, ReportSupersede);

/// Registers the execution and its changed subgraph.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportInit {
    pub exec: State,
}

/// A lifecycle phase transition.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportPhase {
    pub phase: Phase,
    pub label: String,
    pub pct: Option<i64>,
    pub id: String,
    pub pr: i64,
    pub repo: String,
    pub sha: String,
    pub environment: String,
    pub context: String,
    pub log_url: String,
}

/// One stack's runner-told status.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportTick {
    pub stack: String,
    pub status: Status,
    pub detail: String,
}

/// The run failed (innocent stacks still mid-run fold to aborted).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportFail {}

/// The run succeeded.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportSucceed {}

/// Annotates stacks with project, categories, counts and moving state.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportAnnotate {
    pub projects: BTreeMap<String, String>,
    pub categories: BTreeMap<String, Vec<Category>>,
    pub counts: BTreeMap<String, Counts>,
    pub moving: Vec<String>,
}

/// Another execution superseded this one.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReportSupersede {
    pub by: String,
}

/// A past-tense fact produced by [`decide`](super::decide) and folded by
/// [`evolve`](super::evolve).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Event {
    Started(Started),
    PhaseChanged(PhaseChanged),
    StackStatusChanged(StackStatusChanged),
    Failed(Failed),
    Succeeded(Succeeded),
    StacksAnnotated(StacksAnnotated),
    Superseded(Superseded),
}

wrap_variants!(Event: Started, PhaseChanged, StackStatusChanged, Failed, Succeeded,
    StacksAnnotated, Superseded);

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Started {
    #[serde(rename = "Exec", serialize_with = "init_state")]
    pub exec: State,
}

/// The Init state as Go's `Started` event carries it: built by `execInitFromEvents`,
/// whose stack list is nil (`null`) when the runner sent none. The folded state
/// always holds a non-nil list (`[]`), so only the event differs.
fn init_state<S: serde::Serializer>(st: &State, serializer: S) -> Result<S::Ok, S::Error> {
    #[derive(Serialize)]
    struct Wire<'a> {
        #[serde(rename = "ID")]
        id: &'a str,
        #[serde(rename = "Repo")]
        repo: &'a str,
        #[serde(rename = "SHA")]
        sha: &'a str,
        #[serde(rename = "LogURL")]
        log_url: &'a str,
        #[serde(rename = "PR")]
        pr: i64,
        #[serde(rename = "Environment")]
        environment: &'a str,
        #[serde(rename = "Context")]
        context: &'a str,
        #[serde(rename = "Phase")]
        phase: &'a Phase,
        #[serde(rename = "ProgressLabel")]
        progress_label: &'a str,
        #[serde(rename = "ProgressPct")]
        progress_pct: Option<i64>,
        #[serde(rename = "Status")]
        status: &'a str,
        #[serde(rename = "SupersededBy")]
        superseded_by: &'a str,
        #[serde(rename = "Stacks", serialize_with = "null_if_empty")]
        stacks: &'a Vec<Stack>,
        #[serde(rename = "Edges", serialize_with = "null_if_empty")]
        edges: &'a Vec<Edge>,
    }
    Wire {
        id: &st.id,
        repo: &st.repo,
        sha: &st.sha,
        log_url: &st.log_url,
        pr: st.pr,
        environment: &st.environment,
        context: &st.context,
        phase: &st.phase,
        progress_label: &st.progress_label,
        progress_pct: st.progress_pct,
        status: &st.status,
        superseded_by: &st.superseded_by,
        stacks: &st.stacks,
        edges: &st.edges,
    }
    .serialize(serializer)
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct PhaseChanged {
    #[serde(rename = "Phase")]
    pub phase: Phase,
    #[serde(rename = "Label")]
    pub label: String,
    #[serde(rename = "Pct")]
    pub pct: Option<i64>,
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "Repo")]
    pub repo: String,
    #[serde(rename = "SHA")]
    pub sha: String,
    #[serde(rename = "Environment")]
    pub environment: String,
    #[serde(rename = "Context")]
    pub context: String,
    #[serde(rename = "LogURL")]
    pub log_url: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct StackStatusChanged {
    #[serde(rename = "Stack")]
    pub stack: String,
    #[serde(rename = "Status")]
    pub status: Status,
    #[serde(rename = "Detail")]
    pub detail: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Failed {}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Succeeded {}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct StacksAnnotated {
    #[serde(
        rename = "Projects",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub projects: BTreeMap<String, String>,
    #[serde(
        rename = "Categories",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub categories: BTreeMap<String, Vec<Category>>,
    #[serde(
        rename = "Counts",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub counts: BTreeMap<String, Counts>,
    #[serde(
        rename = "Moving",
        deserialize_with = "nullable",
        serialize_with = "null_if_empty"
    )]
    pub moving: Vec<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Superseded {
    #[serde(rename = "By")]
    pub by: String,
}
