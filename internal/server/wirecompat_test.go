package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// TestAPIWireCompat is the wire-compatibility contract for the /api surface:
// every case sends a fixed request and byte-compares status, Content-Type and
// body against a committed golden file (testdata/wire/). The OpenAPI retrofit
// must keep these bytes identical — serve and runner deploy independently, so
// the wire format is a cross-version contract, not an implementation detail.
//
// Regenerate goldens (only for a deliberate, reviewed wire change):
//
//	WIRE_GOLDEN=update go test ./internal/server -run TestAPIWireCompat
//
// SSE endpoints are excluded (no stable byte snapshot); auth rejection shapes
// are included (the runner surfaces them verbatim).
func TestAPIWireCompat(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{
		APIPrincipals: map[string][]string{"runner@x.iam.gserviceaccount.com": {"report", "read", "admin"}},
	})
	a.APIVerifier = fakeOIDC(map[string]string{
		"tok":          "runner@x.iam.gserviceaccount.com",
		"tok-noscopes": "stranger@x.iam.gserviceaccount.com",
	})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	send := func(t *testing.T, method, path, token, body string) *http.Response {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	type step struct {
		name   string
		method string
		path   string
		token  string
		body   string
		// seed runs before the request (fixture setup outside the wire).
		seed func(t *testing.T)
	}

	steps := []step{
		{
			name: "01-init", method: "POST", path: "/api/init", token: "tok",
			body: `{"id":"e1","repo":"o/r","sha":"cafe1234cafe","pr":7,"environment":"staging","log_url":"https://ci/logs/1","stacks":[{"path":"stacks/a","project":"proj-a","status":"pending"},{"path":"stacks/b","project":"proj-b","status":"pending"}],"edges":[{"from":"stacks/a","to":"stacks/b"}]}`,
		},
		{
			name: "02-phase", method: "POST", path: "/api/phase", token: "tok",
			body: `{"id":"e1","phase":"planning"}`,
		},
		{
			name: "03-update", method: "POST", path: "/api/update", token: "tok",
			body: `{"id":"e1","stack":"stacks/a","status":"planned"}`,
		},
		{
			name: "04-update-unknown-status", method: "POST", path: "/api/update", token: "tok",
			body: `{"id":"e1","stack":"stacks/a","status":"bogus"}`,
		},
		{
			name: "05-logs", method: "POST", path: "/api/logs", token: "tok",
			body: `{"id":"e1","stack":"stacks/a","data":"terraform plan says hi\n"}`,
		},
		{
			name: "06-finalize", method: "POST", path: "/api/finalize", token: "tok",
			body: `{"id":"e1","report_markdown":"## report\n","projects":{"stacks/a":"proj-a"},"stack_reports":{"stacks/a":"### stacks/a\n"},"categories":{"stacks/a":[{"name":"iam","icon":"🔐"}]},"counts":{"stacks/a":{"add":1,"change":2}}}`,
		},
		{
			name: "07-gate-check-clean", method: "POST", path: "/api/gate/check", token: "tok",
			body: `{"pr":7,"environment":"staging"}`,
		},
		{
			name: "08-gate-check-not-classified", method: "POST", path: "/api/gate/check", token: "tok",
			body: `{"pr":99,"environment":"staging"}`,
		},
		{
			name: "09-gate-revoke", method: "POST", path: "/api/gate/revoke", token: "tok",
			body: `{"pr":7,"environment":"staging"}`,
		},
		{
			name: "10-claims-list", method: "POST", path: "/api/claims/list", token: "tok",
			body: `{"environment":"staging"}`,
			seed: func(t *testing.T) {
				for _, sp := range []string{"stacks/a", "stacks/b"} {
					if _, err := db.Exec(
						`INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at)
						 VALUES (?,?,?,?,?)`,
						"staging", sp, 7, "e1", fixed); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "11-claims-list-empty", method: "POST", path: "/api/claims/list", token: "tok",
			body: `{"environment":"deserted"}`,
		},
		{
			name: "12-claims-release", method: "POST", path: "/api/claims/release", token: "tok",
			body: `{"environment":"staging","pr":7,"stack":"stacks/a"}`,
		},
		{
			name: "13-execution-get", method: "GET", path: "/api/execution/e2", token: "tok",
			seed: func(t *testing.T) {
				in := events.Init{
					ID: "e2", Repo: "o/r", SHA: "beef5678beef", PR: 8, Environment: "staging",
					LogURL: "https://ci/logs/2",
					Stacks: []events.StackState{{Path: "stacks/c", Project: "proj-c", Status: events.StatusPlanned}},
				}
				seedInit(t, a.shell, in)
				seedProjectionTarget(t, db, 8, "staging", "iam", "proj-c", "projects/proj-c/locations/global/entitlements/iam/grants/g1", "ACTIVE", "req@x.iam.gserviceaccount.com")
			},
		},
		{
			name: "14-execution-get-lifecycle", method: "GET", path: "/api/execution/e1", token: "tok",
		},
		{
			name: "15-execution-404", method: "GET", path: "/api/execution/nope", token: "tok",
		},
		{
			// A change outside the stack tree: zero stacks. The graph arrays
			// must be [] (never null — it crashed the UI live on run-759).
			name: "15b-execution-zero-stacks", method: "GET", path: "/api/execution/e3", token: "tok",
			seed: func(t *testing.T) {
				seedInit(t, a.shell, events.Init{
					ID: "e3", Repo: "o/r", SHA: "0000aaaa0000", PR: 9, Environment: "staging",
					LogURL: "https://ci/logs/3",
				})
				if _, err := db.Exec(`UPDATE executions SET created_at = ? WHERE id = 'e3'`, fixed); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "16-unauthorized", method: "POST", path: "/api/claims/list", token: "",
			body: `{"environment":"staging"}`,
		},
		{
			name: "17-forbidden", method: "POST", path: "/api/claims/list", token: "tok-noscopes",
			body: `{"environment":"staging"}`,
		},
		{
			name: "18-executions-list", method: "GET", path: "/api/executions", token: "tok",
		},
		{
			name: "19-executions-pr-filter", method: "GET", path: "/api/executions?pr=7&limit=1", token: "tok",
		},
		{
			name: "20-executions-bad-limit", method: "GET", path: "/api/executions?limit=0", token: "tok",
		},
		{
			name: "21-approvals", method: "GET", path: "/api/approvals", token: "tok",
			seed: func(t *testing.T) {
				seedProjectionTarget(t, db, 8, "staging", "destructive", "proj-c", "", "AWAITING", "req@x.iam.gserviceaccount.com")
			},
		},
		{
			name: "22-pr-get", method: "GET", path: "/api/pr/7", token: "tok",
			seed: func(t *testing.T) {
				if err := store.UpsertPRMeta(db, store.PRMeta{
					Repo: "o/r", PR: 7, Title: "Add widget", Body: "Does widget things.",
					AuthorLogin: "octocat", HeadRef: "feature/widget",
					URL: "https://github.com/o/r/pull/7", AutoMerge: true,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "23-pr-404", method: "GET", path: "/api/pr/424242", token: "tok",
		},
		{
			// The normal window right after a PR opens: the webhook has
			// written pr_meta, but the runner has not yet called /api/init
			// for this PR — no execution exists. Must be 200 with meta
			// populated, not 404 (the bug this step guards against).
			name: "24-pr-get-meta-only-no-execution", method: "GET", path: "/api/pr/555", token: "tok",
			seed: func(t *testing.T) {
				if err := store.UpsertPRMeta(db, store.PRMeta{
					Repo: "o/r", PR: 555, Title: "Pre-execution PR", Body: "Meta arrives before init.",
					AuthorLogin: "octocat", HeadRef: "feature/pre-execution",
					URL: "https://github.com/o/r/pull/555", AutoMerge: false,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if s.seed != nil {
				s.seed(t)
			}
			if strings.HasPrefix(s.name, "13-") || strings.HasPrefix(s.name, "14-") {
				// Pin DB-assigned timestamps so execution reads are byte-stable.
				if _, err := db.Exec(`UPDATE executions SET created_at = ?`, fixed); err != nil {
					t.Fatal(err)
				}
			}
			resp := send(t, s.method, s.path, s.token, s.body)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			got := renderWire(resp.StatusCode, resp.Header.Get("Content-Type"), body)
			golden := filepath.Join("testdata", "wire", s.name+".golden")
			if os.Getenv("WIRE_GOLDEN") == "update" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with WIRE_GOLDEN=update to create): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("wire mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", s.name, got, want)
			}
		})
	}
}

// renderWire snapshots the parts of a response that form the wire contract:
// status, Content-Type, and the exact body bytes.
func renderWire(status int, contentType string, body []byte) []byte {
	if contentType == "" {
		contentType = "(none)"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP %d\nContent-Type: %s\n\n", status, contentType)
	b.Write(body)
	return b.Bytes()
}
