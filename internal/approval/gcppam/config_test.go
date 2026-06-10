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
