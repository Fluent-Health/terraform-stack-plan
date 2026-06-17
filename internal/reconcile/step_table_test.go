package reconcile

import (
	"sort"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// actionKinds returns sorted action type names for stable comparison.
func actionKinds(actions []Action) []string {
	var out []string
	for _, a := range actions {
		switch a.(type) {
		case RequestGrant:
			out = append(out, "RequestGrant")
		case RevokeGrant:
			out = append(out, "RevokeGrant")
		case RenderCheckRun:
			out = append(out, "RenderCheckRun")
		case PostCommitStatus:
			out = append(out, "PostCommitStatus")
		case PublishSSE:
			out = append(out, "PublishSSE")
		}
	}
	sort.Strings(out)
	return out
}

func gateKind(g GateState) string {
	switch g.(type) {
	case NotClassified:
		return "NotClassified"
	case Clean:
		return "Clean"
	case Pending:
		return "Pending"
	case Satisfied:
		return "Satisfied"
	case Blocked:
		return "Blocked"
	}
	return "?"
}

func TestStepTable(t *testing.T) {
	active := func(tgt string) Target {
		return Target{Class: "iam", Target: tgt, GrantName: "g-" + tgt, Grant: approval.StateActive}
	}
	cases := []struct {
		name      string
		prior     ChangeSet
		signal    Signal
		wantGate  string
		wantKinds []string
	}{
		// ── BASE ROWS (from task spec) ─────────────────────────────────────────────

		{
			name:      "Bug#1: PRClosed on Satisfied revokes + terminalizes",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    PRClosed{},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RevokeGrant"},
		},
		{
			name:      "Bug#2: abandoned foreign collision revokes blocker + retries",
			prior:     ChangeSet{PR: 8, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 7, ByEnv: "staging", ByPRAbandoned: true}}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant", "RevokeGrant"},
		},
		{
			name:      "clobber: tick on Satisfied preserves lease, stays Satisfied",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g-p1", State: approval.StateActive}}},
			wantGate:  "Satisfied",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "gap①: ACTIVE grant gone downgrades to Blocked",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", State: ""}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "gap②: re-plan subset prunes dropped target",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Lease: Lease{Requester: "sa0"}, Targets: []Target{active("p1"), {Class: "iam", Target: "p2", GrantName: "g2", Grant: approval.StateDenied}}}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RevokeGrant"},
		},
		{
			name:      "gap③: DENIED becomes Blocked{denied}",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateDenied}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "gap⑥: self collision blocks without revoke",
			prior:     ChangeSet{PR: 8, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 8, ByEnv: "prod", BySelf: true}}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "clean plan → Clean, success",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}},
			signal:    RunnerFinalize{Gates: nil},
			wantGate:  "Clean",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// ── EXTENDED ROWS ─────────────────────────────────────────────────────────
		// Each group is labelled with the axes it adds coverage for.

		// signal=RunnerInit; prior gate=nil→NotClassified; grantObs=none
		{
			name: "RunnerInit: seeds exec, gate becomes NotClassified",
			prior: ChangeSet{PR: 10, Environment: "prod",
				Exec: Execution{ID: "", Stacks: []Stack{{Path: "s/a", RunStatus: events.StatusPending}}}},
			signal:    RunnerInit{Exec: Execution{ID: "e1", Repo: "r", SHA: "abc", Stacks: []Stack{{Path: "s/a", RunStatus: events.StatusPending}}}},
			wantGate:  "NotClassified",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerPhase; prior gate=NotClassified; grantObs=none
		{
			name:      "RunnerPhase: emits Render+SSE, gate unchanged NotClassified",
			prior:     ChangeSet{PR: 10, Environment: "prod", Gate: NotClassified{}, Exec: Execution{Phase: events.PhasePlanning}},
			signal:    RunnerPhase{Phase: events.PhaseApplying},
			wantGate:  "NotClassified",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerUpdate; prior gate=Pending; grantObs=none
		{
			name: "RunnerUpdate: emits Render+SSE, gate unchanged Pending",
			prior: ChangeSet{PR: 10, Environment: "prod", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}},
				Exec: Execution{Stacks: []Stack{{Path: "s/a", RunStatus: events.StatusRunning}}}},
			signal:    RunnerUpdate{Stack: "s/a", Status: events.StatusPlanned, Detail: ""},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerFinalize{Failed:true}; prior gate=Pending; grantObs=none
		{
			name: "RunnerFinalize Failed: gate stays Pending, emits failure render",
			prior: ChangeSet{PR: 7, Environment: "staging",
				Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}},
				Exec: Execution{Stacks: []Stack{{Path: "s1", RunStatus: events.StatusRunning}}}},
			signal:    RunnerFinalize{Failed: true},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerFinalize{Gates:[superset]}; prior gate=Pending with one target; signalGates=superset
		{
			name:  "RunnerFinalize superset gates: adds new target, requests first ungranted",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{active("p1")}}},
			signal: RunnerFinalize{Gates: []events.GateTarget{
				{Class: "iam", Target: "p1"},
				{Class: "iam", Target: "p2"},
			}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},

		// signal=RunnerFinalize{Gates:same-as-stored}; prior gate=Pending; signalGates=same
		{
			name:      "RunnerFinalize same gates: carries forward existing targets, requests first ungranted",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},

		// signal=RunnerFinalize{Gates:disjoint}; prior gate=Pending; signalGates=disjoint; grantName present
		{
			name:  "RunnerFinalize disjoint gates: revokes old granted target, requests new one",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{active("p1")}}},
			signal: RunnerFinalize{Gates: []events.GateTarget{
				{Class: "iam", Target: "p99"},
			}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant", "RevokeGrant"},
		},

		// signal=GrantsObserved; grantObs=AWAITING (single target stays Pending)
		{
			name:      "GrantsObserved AWAITING: stays Pending, no new RequestGrant when grant name assigned",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateAwaiting, Requester: "sa1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=GrantsObserved; grantObs=ACTIVATING (single target stays Pending)
		{
			name:      "GrantsObserved ACTIVATING: stays Pending, no new request",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActivating, Requester: "sa1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=GrantsObserved; grantObs=ACTIVE (all active → Satisfied)
		{
			name:      "GrantsObserved all ACTIVE: becomes Satisfied",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Lease: Lease{Requester: "sa1"}, Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive}}},
			wantGate:  "Satisfied",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=GateTick; grantObs=EXPIRED on previously-active → Blocked via prevWasActive path
		{
			name:      "GateTick EXPIRED on prior-active: Blocked via prevWasActive",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g-p1", State: approval.StateExpired}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=GateTick; grantObs=REVOKED on previously-active → Blocked (terminal denial path)
		{
			name:      "GateTick REVOKED: Blocked{revoked}",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g-p1", State: approval.StateRevoked}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=PRClosed; prior gate=NotClassified → no-op (open vs closed PR axis; grantObs=none)
		{
			name:      "PRClosed on NotClassified: no-op",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}},
			signal:    PRClosed{},
			wantGate:  "NotClassified",
			wantKinds: nil,
		},

		// signal=PRClosed; prior gate=Clean → no-op
		{
			name:      "PRClosed on Clean: no-op",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Clean{}},
			signal:    PRClosed{},
			wantGate:  "Clean",
			wantKinds: nil,
		},

		// signal=PRClosed; prior gate=Pending with grants → revokes + Blocked
		{
			name:      "PRClosed on Pending with grant: revokes + Blocked{revoked}",
			prior:     ChangeSet{PR: 9, Environment: "prod", Gate: Pending{Lease: Lease{Requester: "sa2"}, Targets: []Target{active("proj-a")}}},
			signal:    PRClosed{},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RevokeGrant"},
		},

		// signal=PRClosed; prior gate=Blocked with grants → revokes + stays Blocked{revoked}
		{
			name:      "PRClosed on Blocked with grants: revokes + Blocked{revoked}",
			prior:     ChangeSet{PR: 9, Environment: "prod", Gate: Blocked{Lease: Lease{Requester: "sa2"}, Targets: []Target{active("proj-a")}, By: Blocker{Reason: ReasonDenied}}},
			signal:    PRClosed{},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RevokeGrant"},
		},

		// signal=ApplySucceeded; prior gate=Satisfied → Clean, RevokeGrant only
		{
			name:      "ApplySucceeded on Satisfied: Clean, RevokeGrant no Render/SSE",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    ApplySucceeded{},
			wantGate:  "Clean",
			wantKinds: []string{"RevokeGrant"},
		},

		// signal=ApplySucceeded; prior gate=Pending with grants → Clean, RevokeGrant only
		{
			name:      "ApplySucceeded on Pending with grant: Clean, RevokeGrant only",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Lease: Lease{Requester: "sa1"}, Targets: []Target{active("p1")}}},
			signal:    ApplySucceeded{},
			wantGate:  "Clean",
			wantKinds: []string{"RevokeGrant"},
		},

		// signal=ApplySucceeded; prior gate=NotClassified → no-op
		{
			name:      "ApplySucceeded on NotClassified: no-op",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}},
			signal:    ApplySucceeded{},
			wantGate:  "NotClassified",
			wantKinds: nil,
		},

		// signal=ApplySucceeded; prior gate=Clean → no-op
		{
			name:      "ApplySucceeded on Clean: no-op",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Clean{}},
			signal:    ApplySucceeded{},
			wantGate:  "Clean",
			wantKinds: nil,
		},

		// signal=ApplySucceeded; prior gate=Blocked with grants → Clean, revoke only
		{
			name:      "ApplySucceeded on Blocked with grants: Clean, RevokeGrant only",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Blocked{Lease: Lease{Requester: "sa1"}, Targets: []Target{active("p1")}, By: Blocker{Reason: ReasonDenied}}},
			signal:    ApplySucceeded{},
			wantGate:  "Clean",
			wantKinds: []string{"RevokeGrant"},
		},

		// lease=undecided("") → Pending with empty Requester on first finalize
		{
			name:      "Pending lease undecided: finalize with gates, request unpinned (no requester)",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},

		// lease=leased → GrantsObserved carries prior lease forward; stays Pending with pinned requester
		{
			name:      "GrantsObserved with existing lease: pins requester from prior Pending lease",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Lease: Lease{Requester: "sa5"}, Targets: []Target{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateAwaiting, Requester: "sa5"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},

		// slot=free (normal request, no collision)
		{
			name:      "slot free: GrantsObserved no collision, observe AWAITING stays Pending",
			prior:     ChangeSet{PR: 11, Environment: "dev", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p3"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p3", Name: "gx", State: approval.StateAwaiting, Requester: "sa9"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// slot=held-by-other-open (foreign open collision → Blocked{slot_foreign})
		{
			name:      "slot held-by-other-open: open foreign collision → Blocked{slot_foreign}",
			prior:     ChangeSet{PR: 8, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 5, ByEnv: "staging", ByPRAbandoned: false}}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// blockerPR=self vs blockerPR=other — self collision
		{
			name:      "blockerPR self (GateTick): Blocked{slot_self}",
			prior:     ChangeSet{PR: 8, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 8, ByEnv: "prod", BySelf: true}}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// Multiple targets: two active → Satisfied
		{
			name: "multi-target: both ACTIVE → Satisfied",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
				Lease:   Lease{Requester: "sa1"},
				Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}, {Class: "iam", Target: "p2", GrantName: "g2", Grant: approval.StateAwaiting}},
			}},
			signal: GrantsObserved{Grants: []ObservedGrant{
				{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive},
				{Class: "iam", Target: "p2", Name: "g2", State: approval.StateActive},
			}},
			wantGate:  "Satisfied",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// Multiple targets: one denied → Blocked{denied}
		{
			name: "multi-target: one DENIED → Blocked{denied}",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
				Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1"}, {Class: "iam", Target: "p2", GrantName: "g2"}},
			}},
			signal: GrantsObserved{Grants: []ObservedGrant{
				{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive},
				{Class: "iam", Target: "p2", Name: "g2", State: approval.StateDenied},
			}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerFinalize{Gates:nil} on Clean prior: re-plan with no gates stays Clean
		{
			name:      "RunnerFinalize no gates on Clean prior: stays Clean",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Clean{}},
			signal:    RunnerFinalize{Gates: nil},
			wantGate:  "Clean",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		// signal=RunnerFinalize on Blocked prior: re-plan with same target re-enters Pending
		{
			name:  "RunnerFinalize on Blocked prior: replans same target, re-enters Pending",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Blocked{Targets: []Target{active("p1")}, By: Blocker{Reason: ReasonDenied}}},
			signal: RunnerFinalize{Gates: []events.GateTarget{
				{Class: "iam", Target: "p1"},
			}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},

		{
			name: "B: re-plan re-arms a REVOKED target (was wedged)",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Blocked{
				Lease:   Lease{Requester: "sa1"},
				Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateRevoked}},
				By:      Blocker{Reason: ReasonRevoked},
			}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},
		{
			name: "B: re-plan re-arms an EXPIRED target",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
				Lease:   Lease{Requester: "sa1"},
				Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateExpired}},
			}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},
		{
			name: "B: re-plan re-arms a DENIED target",
			prior: ChangeSet{PR: 7, Environment: "staging", Gate: Blocked{
				Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateDenied}},
				By:      Blocker{Reason: ReasonDenied},
			}},
			signal:    RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},

		{
			name:      "partial downgrade: one of two ACTIVE targets expires on tick",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1"), active("p2")}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g-p1", State: approval.StateActive}, {Class: "iam", Target: "p2", Name: "g-p2", State: approval.StateExpired}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "lease established from a later observation (first has no requester)",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}, {Class: "iam", Target: "p2"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateAwaiting}, {Class: "iam", Target: "p2", Name: "g2", State: approval.StateAwaiting, Requester: "sa9"}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "terminal-block wins over an ungranted target",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}}}},
			signal:    GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateDenied}}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
		{
			name:      "EXPIRED on a never-active Pending target is re-armed (re-requested)",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Pending{Lease: Lease{Requester: "sa3"}, Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}}}},
			signal:    GateTick{Grants: []ObservedGrant{{Class: "iam", Target: "p1", Name: "g1", State: approval.StateExpired}}},
			wantGate:  "Pending",
			wantKinds: []string{"PublishSSE", "RenderCheckRun", "RequestGrant"},
		},
		{
			name:      "GateTick absent target downgrades Satisfied (full re-list)",
			prior:     ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{active("p1")}}},
			signal:    GateTick{Grants: []ObservedGrant{}},
			wantGate:  "Blocked",
			wantKinds: []string{"PublishSSE", "RenderCheckRun"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, actions := Step(World{Prior: c.prior}, c.signal)
			if gateKind(got.Gate) != c.wantGate {
				t.Fatalf("gate=%s want %s", gateKind(got.Gate), c.wantGate)
			}
			kinds := actionKinds(actions)
			if !equalStrings(kinds, c.wantKinds) {
				t.Fatalf("actions=%v want %v", kinds, c.wantKinds)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
