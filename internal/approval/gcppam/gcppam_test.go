package gcppam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// fakePAM stands up an httptest PAM server and returns a Backend pointed at it
// with stub token funcs.
func fakePAM(t *testing.T, h http.HandlerFunc) *Backend {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{
		BaseURL:       srv.URL,
		Entitlements:  map[string]string{"iam": "iam-elev"},
		RequesterPool: []string{"sa0", "sa1"},
	}
	return New(cfg,
		func(context.Context) (string, error) { return "adc-token", nil },
		func(_ context.Context, sa string) (string, error) { return "imp-" + sa, nil },
	)
}

func TestListGrantsMapsAndCorrelates(t *testing.T) {
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/entitlements/iam-elev/grants") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer adc-token" {
			t.Errorf("list should use ADC token, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"grants":[
			{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/g1","state":"ACTIVE","justification":{"unstructuredJustification":"PR #42 env=staging"}},
			{"name":"…/grants/g2","state":"APPROVAL_AWAITED","justification":{"unstructuredJustification":"PR #7 env=prod"}}
		]}`))
	})
	got, err := b.ListGrants(context.Background(), "iam", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d grants, want 2", len(got))
	}
	if got[0].State != approval.StateActive || got[0].Request.PR != 42 || got[0].Request.Environment != "staging" {
		t.Errorf("grant0 = %+v", got[0])
	}
	if got[1].State != approval.StateAwaiting || got[1].Request.PR != 7 {
		t.Errorf("grant1 = %+v", got[1])
	}
	if got[0].Request.Class != "iam" || got[0].Request.Target != "proj-a" {
		t.Errorf("grant0 request class/target = %+v", got[0].Request)
	}
}

func TestListGrantsUnconfiguredClass(t *testing.T) {
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) { t.Error("should not call PAM") })
	if _, err := b.ListGrants(context.Background(), "database", "proj-a"); err == nil {
		t.Error("want error for class with no configured entitlement")
	}
}
