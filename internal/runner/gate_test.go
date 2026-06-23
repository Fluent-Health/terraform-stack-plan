package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGateCheckVerdicts(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantKind VerdictKind
		wantReq  string
	}{
		{"satisfied", 200, `{"requester":"sa@x"}`, VerdictSatisfied, "sa@x"},
		{"not-classified", 409, `{"code":"GATE-001","message":"not classified"}`, VerdictNotClassified, ""},
		{"not-satisfied", 409, `{"code":"GATE-002","message":"gate not satisfied"}`, VerdictNotSatisfied, ""},
		{"unconfirmable", 503, `{"code":"GATE-003","message":"x"}`, VerdictUnconfirmable, ""},
		{"unknown-code-fails-closed", 409, `{"code":"ZZZ-999","message":"x"}`, VerdictNotSatisfied, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "")
			v := c.GateCheck(context.Background(), events.GateCheck{PR: 1, Environment: "nonprod"})
			if v.Kind != tc.wantKind {
				t.Fatalf("kind = %v, want %v", v.Kind, tc.wantKind)
			}
			if v.Requester != tc.wantReq {
				t.Fatalf("requester = %q, want %q", v.Requester, tc.wantReq)
			}
			if v.Allowed() != (tc.wantKind == VerdictSatisfied) {
				t.Fatalf("Allowed() = %v for kind %v", v.Allowed(), tc.wantKind)
			}
		})
	}
}

func TestGateCheckUnreachableIsFailClosed(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "") // nothing listening
	v := c.GateCheck(context.Background(), events.GateCheck{PR: 1, Environment: "nonprod"})
	if v.Kind != VerdictUnreachable {
		t.Fatalf("kind = %v, want VerdictUnreachable", v.Kind)
	}
	if v.Allowed() {
		t.Fatal("unreachable verdict must not be Allowed()")
	}
}

func TestGateCheckDisabledClientIsSatisfied(t *testing.T) {
	c := NewClient("", "") // no server configured ⇒ nothing gates
	v := c.GateCheck(context.Background(), events.GateCheck{PR: 1, Environment: "nonprod"})
	if !v.Allowed() {
		t.Fatal("disabled client must be Allowed() (nothing gates)")
	}
}
