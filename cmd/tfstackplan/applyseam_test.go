package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// errBoom is a sentinel error used by apply tests to simulate a failing
// terramate script run or state-move pre-phase.
var errBoom = errors.New("boom")

// fakeTM is a test double for the terramate operations runApply drives. It lets
// the cmd-level apply tests exercise the orchestration (ordering, finalize,
// gate) without a real terramate/terraform on PATH.
type fakeTM struct {
	changed   []string
	all       []string
	edges     []events.Edge
	scriptErr error
	scriptRan bool
	gotOpts   runner.ScriptRunOptions
}

func (f *fakeTM) ChangedStacks(_ context.Context, _ string) ([]string, error) { return f.changed, nil }
func (f *fakeTM) List(_ context.Context) ([]string, error)                    { return f.all, nil }
func (f *fakeTM) RunGraph(_ context.Context) ([]events.Edge, error)           { return f.edges, nil }
func (f *fakeTM) ScriptRun(_ context.Context, _ io.Writer, o runner.ScriptRunOptions) error {
	f.scriptRan = true
	f.gotOpts = o
	return f.scriptErr
}

// recordingServer is an HTTP stub that records request paths (in order) and the
// decoded finalize payloads so tests can assert call ordering and contents.
type recordingServer struct {
	mu        sync.Mutex
	order     []string
	finalizes []events.Finalize
	// gateCheckStatus is the HTTP status the /api/gate/check handler returns
	// (default 200). When gateCheckOnce is set, the first call uses
	// gateCheckStatus and subsequent calls return 200 (simulates classify-then-pass).
	gateCheckStatus int
	gateCheckOnce   bool
	gateCheckHits   int
}

func (rs *recordingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.order = append(rs.order, r.URL.Path)
		switch r.URL.Path {
		case "/api/finalize":
			var f events.Finalize
			_ = json.Unmarshal(b, &f)
			rs.finalizes = append(rs.finalizes, f)
		case "/api/gate/check":
			rs.gateCheckHits++
			status := rs.gateCheckStatus
			if status == 0 {
				status = 200
			}
			if rs.gateCheckOnce && rs.gateCheckHits > 1 {
				status = 200
			}
			rs.mu.Unlock()
			if status != 200 {
				http.Error(w, "gate not satisfied", status)
			} else {
				w.WriteHeader(200)
			}
			return
		}
		rs.mu.Unlock()
		w.WriteHeader(200)
	}
}

func (rs *recordingServer) has(path string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, p := range rs.order {
		if p == path {
			return true
		}
	}
	return false
}

// orderedBefore reports whether the first occurrence of a precedes the first
// occurrence of b (both must occur).
func (rs *recordingServer) orderedBefore(a, b string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	ai, bi := -1, -1
	for i, p := range rs.order {
		if p == a && ai == -1 {
			ai = i
		}
		if p == b && bi == -1 {
			bi = i
		}
	}
	return ai != -1 && bi != -1 && ai < bi
}

func (rs *recordingServer) finalizeFailed() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, f := range rs.finalizes {
		if f.Failed {
			return true
		}
	}
	return false
}

func (rs *recordingServer) lastFinalize() (events.Finalize, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.finalizes) == 0 {
		return events.Finalize{}, false
	}
	return rs.finalizes[len(rs.finalizes)-1], true
}

// stubServer starts a recording server and registers cleanup.
func stubServer(t *testing.T) (*httptest.Server, *recordingServer) {
	t.Helper()
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	t.Cleanup(srv.Close)
	return srv, rs
}

// setApplyEnv points the runner client at srv and sets the (pr, env) keying.
func setApplyEnv(t *testing.T, url string, pr int, env string) {
	t.Helper()
	t.Setenv(runner.EnvServer, url)
	t.Setenv(runner.EnvEnvironment, env)
	t.Setenv(runner.EnvExecution, "")
	t.Setenv("TFSTACKPLAN_PR", itoa(pr))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// withFakeTM installs a fakeTM via the newTerramate seam for the duration of the
// test, and stubs the classify pass so cmd-level apply tests don't need a real
// plan run. The classify stub returns no gates by default; pass gates to
// simulate an IAM-gated change.
func withFakeTM(t *testing.T, f *fakeTM, gates []events.GateTarget) {
	t.Helper()
	origTM := newTerramate
	newTerramate = func(string) tmRunner { return f }
	t.Cleanup(func() { newTerramate = origTM })

	origCls := classifyForGateFn
	classifyForGateFn = func(_ context.Context, _ string, _ []string, _ string, _ bool, _ string) ([]events.GateTarget, map[string][]events.Category, map[string]events.Counts, []string, string, error) {
		return gates, map[string][]events.Category{}, map[string]events.Counts{}, nil, "classified", nil
	}
	t.Cleanup(func() { classifyForGateFn = origCls })
}
