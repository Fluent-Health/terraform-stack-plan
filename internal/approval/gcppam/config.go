// Package gcppam implements the approval.Backend over GCP Privileged Access
// Manager (PAM). The server requests a time-bound grant per (class, target)
// entitlement, lists grant state, and revokes after apply; humans approve in
// PAM. Everything deployment-specific — the per-class entitlement ids, the
// requester service-account pool, the grant duration — is Config, so the package
// carries no hardcoded names. GCP credential acquisition is injected (see New),
// keeping the package dependency-free and offline-testable.
package gcppam

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

const defaultBaseURL = "https://privilegedaccessmanager.googleapis.com/v1"

// Config is the gcp-pam backend configuration.
type Config struct {
	// BaseURL is the PAM REST base; empty uses the public GCP endpoint.
	BaseURL string
	// Location is the entitlement location; empty defaults to "global".
	Location string
	// Entitlements maps a classification class (e.g. "iam") to the PAM
	// entitlement id requested for that class.
	Entitlements map[string]string
	// EntitlementScopes maps a class to the PAM entitlement resource scope —
	// "projects" (default), "folders", or "organizations". The class's target is
	// the id within that scope. Empty/absent ⇒ "projects" (backward compatible).
	EntitlementScopes map[string]string
	// RequesterPool is the set of service-account identities the backend
	// impersonates when requesting a grant (one leased per PR). Per the PAM model
	// the grant elevates the *requester*, so a pool avoids elevating every
	// concurrent workload that shares one identity.
	RequesterPool []string
	// Duration is the requested grant duration (e.g. "28800s"); empty defaults to
	// 8 hours.
	Duration string
}

func (c Config) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c Config) location() string {
	if c.Location != "" {
		return c.Location
	}
	return "global"
}

func (c Config) duration() string {
	if c.Duration != "" {
		return c.Duration
	}
	return "28800s"
}

// entitlementName builds the PAM entitlement resource for a (class, target):
// <scope>/<target>/locations/<location>/entitlements/<entitlement>, where scope
// is the class's EntitlementScope (default "projects"). Returns "" when the class
// has no configured entitlement.
func (c Config) entitlementName(class, target string) string {
	e := c.Entitlements[class]
	if e == "" {
		return ""
	}
	scope := c.EntitlementScopes[class]
	if scope == "" {
		scope = "projects"
	}
	return fmt.Sprintf("%s/%s/locations/%s/entitlements/%s", scope, target, c.location(), e)
}

// requester returns the pool identity leased for a PR (a simple modulo slot;
// true leasing can replace this without an interface change). "" when the pool
// is empty.
func (c Config) requester(pr int) string {
	n := len(c.RequesterPool)
	if n == 0 {
		return ""
	}
	i := pr % n
	if i < 0 {
		i += n
	}
	return c.RequesterPool[i]
}

// leaseRequester returns the first pool identity not currently leased (i.e. not
// holding an open grant), giving true leasing instead of a fixed pr-mod slot. When
// every identity is leased it falls back to the deterministic mod slot.
func (c Config) leaseRequester(pr int, leased map[string]bool) string {
	for _, sa := range c.RequesterPool {
		if !leased[sa] {
			return sa
		}
	}
	return c.requester(pr)
}

// justificationRE matches the justification this backend writes.
var justificationRE = regexp.MustCompile(`PR #(\d+) env=(\S+)`)

// justification is the grant justification encoding the change correlation. PAM
// shows it to the approver and the backend parses it back to map a grant to its
// (PR, environment).
func justification(req approval.Request) string {
	return fmt.Sprintf("PR #%d env=%s", req.PR, req.Environment)
}

// parsePRenv extracts (PR, environment) from a justification.
func parsePRenv(j string) (pr int, environment string, ok bool) {
	m := justificationRE.FindStringSubmatch(j)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[2], true
}

// validEnvironment rejects an environment that cannot round-trip through the
// grant justification ("PR #<n> env=<env>", parsed back with the env=(\S+)
// token): it must be non-empty and contain no whitespace. Empty yields no \S+
// match; embedded whitespace truncates the match — either way grant correlation
// (reuse/revoke) silently misses the grant.
func validEnvironment(env string) error {
	if env == "" {
		return fmt.Errorf("gcppam: environment must not be empty")
	}
	if strings.IndexFunc(env, unicode.IsSpace) >= 0 {
		return fmt.Errorf("gcppam: environment %q must not contain whitespace (breaks grant correlation)", env)
	}
	return nil
}

// mapState maps a PAM grant state to the normalised approval.GrantState. An
// unrecognised state passes through as-is (and is therefore treated as closed,
// since GrantState.Open only recognises the normalised open states).
func mapState(pamState string) approval.GrantState {
	switch pamState {
	case "APPROVAL_AWAITED", "SCHEDULED":
		return approval.StateAwaiting
	case "ACTIVATING":
		return approval.StateActivating
	case "ACTIVE":
		return approval.StateActive
	case "DENIED":
		return approval.StateDenied
	case "REVOKED":
		return approval.StateRevoked
	case "EXPIRED", "ENDED":
		return approval.StateExpired
	default:
		return approval.GrantState(pamState)
	}
}
