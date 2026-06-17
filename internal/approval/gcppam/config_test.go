package gcppam

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

func TestConfigDefaults(t *testing.T) {
	var c Config
	if c.baseURL() != "https://privilegedaccessmanager.googleapis.com/v1" {
		t.Errorf("baseURL default = %q", c.baseURL())
	}
	if c.location() != "global" {
		t.Errorf("location default = %q", c.location())
	}
	if c.duration() != "28800s" {
		t.Errorf("duration default = %q", c.duration())
	}
	c2 := Config{BaseURL: "http://x", Location: "us", Duration: "60s"}
	if c2.baseURL() != "http://x" || c2.location() != "us" || c2.duration() != "60s" {
		t.Errorf("overrides not honored: %+v", c2)
	}
}

func TestEntitlementName(t *testing.T) {
	c := Config{Entitlements: map[string]string{"iam": "iam-elev"}}
	got := c.entitlementName("iam", "proj-a")
	if got != "projects/proj-a/locations/global/entitlements/iam-elev" {
		t.Errorf("entitlementName = %q", got)
	}
	if c.entitlementName("database", "proj-a") != "" {
		t.Error("unconfigured class should yield empty entitlement")
	}
}

func TestRequester(t *testing.T) {
	c := Config{RequesterPool: []string{"sa0", "sa1", "sa2"}}
	if c.requester(7) != "sa1" {
		t.Errorf("requester(7) = %q, want sa1", c.requester(7))
	}
	if c.requester(0) != "sa0" {
		t.Errorf("requester(0) = %q", c.requester(0))
	}
	if (Config{}).requester(3) != "" {
		t.Error("empty pool → empty requester")
	}
}

func TestLeaseRequester(t *testing.T) {
	c := Config{RequesterPool: []string{"sa0", "sa1", "sa2"}}
	// sa0 leased → pick the first free (sa1).
	if got := c.leaseRequester(0, map[string]bool{"sa0": true}); got != "sa1" {
		t.Errorf("leaseRequester = %q, want sa1", got)
	}
	// none leased → first pool identity.
	if got := c.leaseRequester(5, nil); got != "sa0" {
		t.Errorf("leaseRequester(none leased) = %q, want sa0", got)
	}
	// all leased → fall back to the pr-mod slot (pr=4 → pool[4%3]=sa1).
	all := map[string]bool{"sa0": true, "sa1": true, "sa2": true}
	if got := c.leaseRequester(4, all); got != "sa1" {
		t.Errorf("leaseRequester(all leased, pr 4) = %q, want sa1 (mod fallback)", got)
	}
}

func TestJustificationRoundTrip(t *testing.T) {
	req := approval.Request{Class: "iam", Target: "proj-a", PR: 42, Environment: "staging"}
	j := justification(req)
	if j != "PR #42 env=staging" {
		t.Errorf("justification = %q", j)
	}
	pr, env, ok := parsePRenv(j)
	if !ok || pr != 42 || env != "staging" {
		t.Errorf("parsePRenv = %d/%q/%v", pr, env, ok)
	}
	if _, _, ok := parsePRenv("nonsense"); ok {
		t.Error("parsePRenv should fail on garbage")
	}
}

func TestMapState(t *testing.T) {
	cases := map[string]approval.GrantState{
		"APPROVAL_AWAITED": approval.StateAwaiting,
		"SCHEDULED":        approval.StateAwaiting,
		"ACTIVATING":       approval.StateActivating,
		"ACTIVE":           approval.StateActive,
		"DENIED":           approval.StateDenied,
		"REVOKED":          approval.StateRevoked,
		"EXPIRED":          approval.StateExpired,
		"ENDED":            approval.StateExpired,
	}
	for in, want := range cases {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q) = %q, want %q", in, got, want)
		}
	}
	if mapState("WEIRD").Open() {
		t.Error("unknown state should be closed")
	}
}

func TestValidEnvironment(t *testing.T) {
	cases := []struct {
		env string
		ok  bool
	}{
		{"staging", true},
		{"prod", true},
		{"fh-dev-svc", true},
		{"", false},
		{" ", false},
		{"stag ing", false},
		{"a\tb", false},
		{"x\n", false},
	}
	for _, c := range cases {
		err := validEnvironment(c.env)
		if (err == nil) != c.ok {
			t.Errorf("validEnvironment(%q) err=%v, want ok=%v", c.env, err, c.ok)
		}
	}
}

func TestEntitlementNameScope(t *testing.T) {
	c := Config{
		Location: "global",
		Entitlements: map[string]string{
			"iam":      "iam-ent",
			"database": "db-ent",
			"org":      "org-ent",
		},
		EntitlementScopes: map[string]string{
			"database": "folders",
			"org":      "organizations",
			// "iam" omitted → defaults to projects
		},
	}
	cases := map[string]string{
		"iam":      "projects/proj-1/locations/global/entitlements/iam-ent",
		"database": "folders/proj-1/locations/global/entitlements/db-ent",
		"org":      "organizations/proj-1/locations/global/entitlements/org-ent",
	}
	for class, want := range cases {
		if got := c.entitlementName(class, "proj-1"); got != want {
			t.Errorf("entitlementName(%q) = %q, want %q", class, got, want)
		}
	}
	if got := c.entitlementName("missing", "proj-1"); got != "" {
		t.Errorf("unconfigured class = %q, want \"\"", got)
	}
}
