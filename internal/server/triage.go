package server

import (
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// failureTriage is the actionable diagnosis of a failed stack, rendered on the
// live page and mirrored into the apply check run. Cause is "" for an unmatched
// error (the raw Detail is shown instead — never a fabricated guess).
type failureTriage struct {
	Class       string   // iam_denied | state_lock | quota | already_exists | state_move | provider_auth | error
	Cause       string   // plain-language likely cause ("" when unknown)
	Steps       []string // ordered next steps (human text; the renderer adds links)
	StateImpact string   // safe-to-retry / partial / move-restore note
	Retryable   bool
}

// matcher is one bounded rule: if any (lowercased) needle is in the detail, it fires.
type matcher struct {
	class   string
	needles []string
	cause   string
	steps   []string
	impact  string
	retry   bool
}

// failureMatchers are tried in order; first match wins. Deliberately small — extend
// with a row + a test, never a DSL.
//
// Ordering rationale:
//   - state_move first: a move failure may also mention permissions; we want the
//     move diagnosis, not an IAM one.
//   - quota before iam_denied: both can match "error 403"; quota's "quota exceeded"
//     needle is more specific and must win.
//   - iam_denied after quota so "error 403" in a plain permission denial still fires.
//   - state_lock, already_exists, provider_auth have non-overlapping signals.
var failureMatchers = []matcher{
	{
		class:   "state_move",
		needles: []string{"cross-state move failed", "state_move_failed", ".tfsp-state-backups"},
		cause:   "A cross-state move failed partway. The source state was rolled back to its pre-move contents (or, if that failed, the error names the backup dir for manual restore).",
		steps:   []string{"Confirm the source/dest state from the error", "Re-run the apply once the move can complete", "If the error names .tfsp-state-backups, restore from there"},
		impact:  "Cross-state move — source restored from backup; re-run to complete (or restore manually from .tfsp-state-backups).",
		retry:   true,
	},
	{
		class:   "quota",
		needles: []string{"quota exceeded", "quotaexceeded", "rate limit", "ratelimitexceeded"},
		cause:   "A cloud quota or rate limit was hit.",
		steps:   []string{"Check the quota named in the error", "Request an increase or wait, then re-run"},
		impact:  "Partially applied up to the limit; re-run continues — review the plan before retrying.",
		retry:   false,
	},
	{
		class:   "iam_denied",
		needles: []string{"setiampolicy", "permission ", "error 403", "permissiondenied", "does not have", "iam.serviceaccount"},
		cause:   "The apply lacked an IAM permission — typically the PAM grant for this target isn't active (not yet approved, or expired mid-apply), so the leased service account can't make this write.",
		steps:   []string{"Re-request elevated access for the target", "Approve the grant, then re-run this tier's apply", "Inspect the full log for the exact permission"},
		impact:  "Safe to retry — denied writes don't land; re-running resumes from the unapplied changes once access is granted.",
		retry:   true,
	},
	{
		class:   "state_lock",
		needles: []string{"acquiring the state lock", "state blob is already locked", "lock info"},
		cause:   "Another run holds the Terraform state lock for this stack.",
		steps:   []string{"Wait for the other run to finish, then re-run", "If it's a stale lock, force-unlock per the runbook"},
		impact:  "Safe to retry once the lock clears — nothing was applied.",
		retry:   true,
	},
	{
		class:   "already_exists",
		needles: []string{"already exists", "error 409", "alreadyexists", "conflict"},
		cause:   "A resource the plan creates already exists out-of-band (drift or a prior partial apply).",
		steps:   []string{"Import the existing resource into state, or remove it", "Re-plan to confirm, then re-run"},
		impact:  "Not auto-retryable — reconcile the conflict first.",
		retry:   false,
	},
	{
		class:   "provider_auth",
		needles: []string{"could not find default credentials", "oauth2", "invalid_grant", "unauthenticated", "error 401"},
		cause:   "The provider couldn't authenticate (token/credentials problem), not a per-resource permission.",
		steps:   []string{"Check the runner's credentials / token mint", "Re-run once auth is restored"},
		impact:  "Safe to retry — nothing was applied.",
		retry:   true,
	},
}

// classifyFailure maps a failed stack's error detail (+ its categories, reserved
// for future tightening) to a triage. Unmatched → raw error + generic recovery,
// with an empty Cause (never a fabricated guess).
func classifyFailure(detail string, cats []events.Category) failureTriage {
	d := strings.ToLower(detail)
	for _, m := range failureMatchers {
		for _, n := range m.needles {
			if strings.Contains(d, n) {
				return failureTriage{Class: m.class, Cause: m.cause, Steps: m.steps, StateImpact: m.impact, Retryable: m.retry}
			}
		}
	}
	return failureTriage{
		Class:       "error",
		Cause:       "",
		Steps:       []string{"Read the error and the full log", "Fix forward and re-run this tier's apply"},
		StateImpact: "Review the log to determine whether any resources were partially applied before retrying.",
		Retryable:   true,
	}
}
