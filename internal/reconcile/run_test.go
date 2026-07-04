package reconcile

import (
	"reflect"
	"testing"
)

// --- Decide: RunRequested ---

func TestDecideRunRequestedQueuesFirstRun(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod"}
	got := Decide(st, RunRequested{Kind: RunKindPlan, SHA: "abcdef1234567890", Branch: "feat/x"})
	want := []Event{RunQueued{
		Kind: RunKindPlan, SHA: "abcdef1234567890", Branch: "feat/x",
		ExecutionID: "run-7-nonprod-plan-abcdef123456-a1", Attempt: 1,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunRequestedRedeliveryNoOps(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", Attempt: 1, Phase: RunPhaseStarted},
	}}
	if got := Decide(st, RunRequested{Kind: RunKindPlan, SHA: "sha1"}); got != nil {
		t.Fatalf("webhook redelivery must no-op, got %#v", got)
	}
}

func TestDecideRunRequestedNewSHASupersedesLivePlan(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e-old", Kind: RunKindPlan, SHA: "oldsha", BuildRef: "b-1", Attempt: 1, Phase: RunPhaseStarted},
	}}
	got := Decide(st, RunRequested{Kind: RunKindPlan, SHA: "newsha", Branch: "feat/x"})
	// Attempt 2: attempts are monotonic across requests (a force-push back to
	// an old SHA must never re-mint its dead execution id).
	want := []Event{
		RunSuperseded{Kind: RunKindPlan, OldExecutionID: "e-old", OldBuildRef: "b-1", NewExecutionID: "run-7-nonprod-plan-newsha-a2", NewSHA: "newsha"},
		RunQueued{Kind: RunKindPlan, SHA: "newsha", Branch: "feat/x", ExecutionID: "run-7-nonprod-plan-newsha-a2", Attempt: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunRequestedNeverDisturbsLiveApply(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindApply: {ExecutionID: "e-apply", Kind: RunKindApply, SHA: "mergesha", Attempt: 1, Phase: RunPhaseStarted},
	}}
	if got := Decide(st, RunRequested{Kind: RunKindApply, SHA: "newermerge"}); got != nil {
		t.Fatalf("live apply must never be superseded, got %#v", got)
	}
	if got := Decide(st, RunRequested{Kind: RunKindApply, SHA: "mergesha", Rerun: true}); got != nil {
		t.Fatalf("rerun of a live apply must be dropped, got %#v", got)
	}
}

func TestDecideRunRequestedRerunSupersedesAndBumpsAttempt(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", BuildRef: "b-1", Attempt: 1, Phase: RunPhaseStarted},
	}}
	got := Decide(st, RunRequested{Kind: RunKindPlan, SHA: "sha1", Branch: "feat/x", Rerun: true})
	want := []Event{
		RunSuperseded{Kind: RunKindPlan, OldExecutionID: "e1", OldBuildRef: "b-1", NewExecutionID: "run-7-nonprod-plan-sha1-a2", NewSHA: "sha1"},
		RunQueued{Kind: RunKindPlan, SHA: "sha1", Branch: "feat/x", ExecutionID: "run-7-nonprod-plan-sha1-a2", Attempt: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunRequestedRetryAfterStartFailure(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindApply: {ExecutionID: "e1", Kind: RunKindApply, SHA: "sha1", Attempt: 1, Phase: RunPhaseStartFailed},
	}}
	got := Decide(st, RunRequested{Kind: RunKindApply, SHA: "sha1", Rerun: true})
	// Terminal prior run: re-queue (no supersede — nothing live to cancel).
	want := []Event{RunQueued{Kind: RunKindApply, SHA: "sha1", ExecutionID: "run-7-nonprod-apply-sha1-a2", Attempt: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunRequestedRejectsGarbage(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod"}
	if got := Decide(st, RunRequested{Kind: "deploy", SHA: "x"}); got != nil {
		t.Fatalf("unknown kind must no-op, got %#v", got)
	}
	if got := Decide(st, RunRequested{Kind: RunKindPlan}); got != nil {
		t.Fatalf("empty sha must no-op, got %#v", got)
	}
}

// --- Decide: run completion (the runner takes over) ---

func TestDecideFinalizeCompletesRun(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", BuildRef: "b-1", Attempt: 1, Phase: RunPhaseStarted},
	}}
	evs := Decide(st, RunnerFinalize{Failed: false})
	found := false
	for _, e := range evs {
		if rc, ok := e.(RunCompleted); ok {
			found = true
			if rc.Kind != RunKindPlan || rc.ExecutionID != "e1" {
				t.Fatalf("RunCompleted = %#v", rc)
			}
		}
	}
	if !found {
		t.Fatalf("plan finalize must complete the live plan run, got %#v", evs)
	}
	// Folding it makes the run non-live, so a new SHA queues WITHOUT a cancel.
	post := st
	for _, e := range evs {
		post = Evolve(post, e)
	}
	got := Decide(post, RunRequested{Kind: RunKindPlan, SHA: "sha2"})
	if len(got) != 1 {
		t.Fatalf("new sha after completion must queue without supersede, got %#v", got)
	}
	if _, ok := got[0].(RunQueued); !ok {
		t.Fatalf("want RunQueued, got %#v", got[0])
	}
}

func TestDecideApplyFinalizeCompletesApplyRun(t *testing.T) {
	// Regression: a completed apply previously stayed Live forever, so every
	// later apply request — including the check Re-run button — was dropped.
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindApply: {ExecutionID: "e-apply", Kind: RunKindApply, SHA: "mergesha", Attempt: 1, Phase: RunPhaseStarted},
	}}
	evs := Decide(st, RunnerFinalize{Failed: true, ApplyContext: true})
	post := st
	for _, e := range evs {
		post = Evolve(post, e)
	}
	if post.Runs[RunKindApply].Live() {
		t.Fatalf("apply run must not stay live after its finalize: %#v", post.Runs[RunKindApply])
	}
	rerun := Decide(post, RunRequested{Kind: RunKindApply, SHA: "mergesha", Rerun: true})
	if len(rerun) != 1 {
		t.Fatalf("re-run of a finished apply must queue, got %#v", rerun)
	}
	q, ok := rerun[0].(RunQueued)
	if !ok || q.Attempt != 2 {
		t.Fatalf("want RunQueued attempt 2, got %#v", rerun[0])
	}
}

func TestDecideFinalizeCompletesStartFailedRun(t *testing.T) {
	// A client-side start "failure" (timeout) where the build actually ran: the
	// runner's finalize proves the start happened — complete the run so the
	// start-failed projection stops touching the row.
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", Attempt: 1, Phase: RunPhaseStartFailed},
	}}
	evs := Decide(st, RunnerFinalize{})
	post := st
	for _, e := range evs {
		post = Evolve(post, e)
	}
	if post.Runs[RunKindPlan].Phase != RunPhaseCompleted {
		t.Fatalf("start-failed run must complete when the runner finalizes: %#v", post.Runs[RunKindPlan])
	}
}

// --- Decide: redelivery / attempt permutations ---

func TestDecideRunRequestedRedeliveryAfterCompletionNoOps(t *testing.T) {
	// A late push redelivery for an apply that already ran must NEVER
	// re-trigger the apply; only an explicit Rerun may.
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindApply: {ExecutionID: "e1", Kind: RunKindApply, SHA: "mergesha", Attempt: 1, Phase: RunPhaseCompleted},
	}}
	if got := Decide(st, RunRequested{Kind: RunKindApply, SHA: "mergesha"}); got != nil {
		t.Fatalf("redelivery after completion must no-op, got %#v", got)
	}
}

func TestDecideRunRequestedForcePushRoundTripMintsFreshIDs(t *testing.T) {
	// Force-push A → B → A: the third request must NOT reuse the first id (its
	// row is superseded/dead in the store) — attempts are monotonic.
	st := ChangeSet{PR: 7, Environment: "nonprod"}
	ids := map[string]bool{}
	shas := []string{"shaA", "shaB", "shaA"}
	for _, sha := range shas {
		evs := Decide(st, RunRequested{Kind: RunKindPlan, SHA: sha})
		for _, e := range evs {
			if q, ok := e.(RunQueued); ok {
				if ids[q.ExecutionID] {
					t.Fatalf("execution id %q minted twice", q.ExecutionID)
				}
				ids[q.ExecutionID] = true
			}
			st = Evolve(st, e)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("want 3 distinct execution ids, got %v", ids)
	}
}

// --- Decide: RunStartResult ---

func TestDecideRunStartResultRecordsStart(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", Phase: RunPhaseQueued},
	}}
	got := Decide(st, RunStartResult{Kind: RunKindPlan, ExecutionID: "e1", BuildRef: "b-42"})
	want := []Event{RunStarted{Kind: RunKindPlan, ExecutionID: "e1", BuildRef: "b-42"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunStartResultRecordsFailure(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", Phase: RunPhaseQueued},
	}}
	got := Decide(st, RunStartResult{Kind: RunKindPlan, ExecutionID: "e1", Err: "trigger not found"})
	want := []Event{RunStartFailed{Kind: RunKindPlan, ExecutionID: "e1", Reason: "trigger not found"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunStartResultDropsStaleFeedback(t *testing.T) {
	st := ChangeSet{PR: 7, Environment: "nonprod", Runs: map[string]Run{
		RunKindPlan: {ExecutionID: "e-new", Kind: RunKindPlan, SHA: "sha2", Phase: RunPhaseQueued},
	}}
	// Feedback for a run that was superseded while starting.
	if got := Decide(st, RunStartResult{Kind: RunKindPlan, ExecutionID: "e-old", BuildRef: "b-1"}); got != nil {
		t.Fatalf("stale start feedback must no-op, got %#v", got)
	}
	if got := Decide(st, RunStartResult{Kind: RunKindApply, ExecutionID: "e-x"}); got != nil {
		t.Fatalf("feedback for an unknown kind must no-op, got %#v", got)
	}
}

// --- Evolve ---

func TestEvolveRunQueuedStoresRun(t *testing.T) {
	got := Evolve(ChangeSet{}, RunQueued{Kind: RunKindPlan, SHA: "sha1", Branch: "b", ExecutionID: "e1", Attempt: 1})
	want := Run{ExecutionID: "e1", Kind: RunKindPlan, SHA: "sha1", Branch: "b", Attempt: 1, Phase: RunPhaseQueued}
	if !reflect.DeepEqual(got.Runs[RunKindPlan], want) {
		t.Fatalf("got %#v want %#v", got.Runs[RunKindPlan], want)
	}
}

func TestEvolveRunStartedSetsBuildRef(t *testing.T) {
	st := Evolve(ChangeSet{}, RunQueued{Kind: RunKindPlan, SHA: "sha1", ExecutionID: "e1", Attempt: 1})
	got := Evolve(st, RunStarted{Kind: RunKindPlan, ExecutionID: "e1", BuildRef: "b-42"})
	r := got.Runs[RunKindPlan]
	if r.Phase != RunPhaseStarted || r.BuildRef != "b-42" {
		t.Fatalf("run = %#v, want started with b-42", r)
	}
	// Stale start for a replaced execution must not fold.
	got2 := Evolve(got, RunStarted{Kind: RunKindPlan, ExecutionID: "e-old", BuildRef: "b-9"})
	if got2.Runs[RunKindPlan].BuildRef != "b-42" {
		t.Fatalf("stale RunStarted folded: %#v", got2.Runs[RunKindPlan])
	}
}

func TestEvolveRunSupersededThenQueuedReplaces(t *testing.T) {
	st := Evolve(ChangeSet{}, RunQueued{Kind: RunKindPlan, SHA: "sha1", ExecutionID: "e1", Attempt: 1})
	st = Evolve(st, RunSuperseded{Kind: RunKindPlan, OldExecutionID: "e1", NewExecutionID: "e2", NewSHA: "sha2"})
	if st.Runs[RunKindPlan].Phase != RunPhaseSuperseded {
		t.Fatalf("run = %#v, want superseded", st.Runs[RunKindPlan])
	}
	st = Evolve(st, RunQueued{Kind: RunKindPlan, SHA: "sha2", ExecutionID: "e2", Attempt: 1})
	r := st.Runs[RunKindPlan]
	if r.ExecutionID != "e2" || r.Phase != RunPhaseQueued {
		t.Fatalf("run = %#v, want e2 queued", r)
	}
}

func TestEvolveRunClonesMap(t *testing.T) {
	prior := Evolve(ChangeSet{}, RunQueued{Kind: RunKindPlan, SHA: "sha1", ExecutionID: "e1", Attempt: 1})
	_ = Evolve(prior, RunStartFailed{Kind: RunKindPlan, ExecutionID: "e1", Reason: "x"})
	if prior.Runs[RunKindPlan].Phase != RunPhaseQueued {
		t.Fatalf("Evolve mutated the prior state's Runs map: %#v", prior.Runs[RunKindPlan])
	}
}

// --- React ---

func TestReactRunQueuedStartsRun(t *testing.T) {
	evs := []Event{RunQueued{Kind: RunKindPlan, SHA: "sha1", Branch: "feat/x", ExecutionID: "e1", Attempt: 1}}
	got := React(ChangeSet{}, evs)
	want := []Action{
		StartRun{Kind: RunKindPlan, SHA: "sha1", Branch: "feat/x", ExecutionID: "e1"},
		RenderCheckRun{},
		PublishSSE{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactRunSupersededCancelsThenStarts(t *testing.T) {
	evs := []Event{
		RunSuperseded{Kind: RunKindPlan, OldExecutionID: "e1", OldBuildRef: "b-1", NewExecutionID: "e2", NewSHA: "sha2"},
		RunQueued{Kind: RunKindPlan, SHA: "sha2", Branch: "feat/x", ExecutionID: "e2", Attempt: 1},
	}
	got := React(ChangeSet{}, evs)
	want := []Action{
		CancelRun{Kind: RunKindPlan, OldExecutionID: "e1", OldBuildRef: "b-1", NewExecutionID: "e2"},
		StartRun{Kind: RunKindPlan, SHA: "sha2", Branch: "feat/x", ExecutionID: "e2"},
		RenderCheckRun{},
		PublishSSE{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactRunStartFailedRendersFailure(t *testing.T) {
	evs := []Event{RunStartFailed{Kind: RunKindPlan, ExecutionID: "e1", Reason: "api error"}}
	got := React(ChangeSet{}, evs)
	want := []Action{
		RenderCheckRun{Terminal: true, Conclusion: "failure"},
		PublishSSE{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactRunStartedRendersInProgress(t *testing.T) {
	evs := []Event{RunStarted{Kind: RunKindPlan, ExecutionID: "e1", BuildRef: "b-1"}}
	got := React(ChangeSet{}, evs)
	want := []Action{RenderCheckRun{}, PublishSSE{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
