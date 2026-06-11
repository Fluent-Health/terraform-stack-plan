package statemove

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

type fakeRunner struct {
	dir       string
	stateJSON string        // returned by StatePull
	show      *tfjson.State // returned by ShowStateFile
	mvs       int
	pushes    int
	pulls     int
	forceUsed bool
}

func (f *fakeRunner) StatePull(context.Context, ...tfexec.StatePullOption) (string, error) {
	f.pulls++
	return f.stateJSON, nil
}
func (f *fakeRunner) ShowStateFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.State, error) {
	return f.show, nil
}
func (f *fakeRunner) StateMv(context.Context, string, string, ...tfexec.StateMvCmdOption) error {
	f.mvs++
	return nil
}
func (f *fakeRunner) StatePush(_ context.Context, _ string, _ ...tfexec.StatePushCmdOption) error {
	f.pushes++
	return nil
}

// stateWith builds a *tfjson.State whose root module has the given addresses.
func stateWith(addrs ...string) *tfjson.State {
	m := &tfjson.StateModule{}
	for _, a := range addrs {
		m.Resources = append(m.Resources, &tfjson.StateResource{Address: a})
	}
	return &tfjson.State{Values: &tfjson.StateValues{RootModule: m}}
}

func depsFor(src, dst *fakeRunner) ExecDeps {
	return ExecDeps{NewTF: func(wd string) (Runner, error) {
		if strings.HasSuffix(wd, "/a") || wd == "a" {
			return src, nil
		}
		return dst, nil
	}}
}

func TestExecuteMovesSourceOnly(t *testing.T) {
	src := &fakeRunner{show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{show: stateWith()}
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}

	// dry-run: no mv, no push.
	if _, err := Execute(context.Background(), depsFor(src, dst), t.TempDir(), "b", xm, true); err != nil {
		t.Fatal(err)
	}
	if src.mvs+dst.mvs != 0 || src.pushes+dst.pushes != 0 {
		t.Errorf("dry-run mutated: mvs=%d pushes=%d", src.mvs+dst.mvs, src.pushes+dst.pushes)
	}

	// execute: one mv + two pushes.
	acts, err := Execute(context.Background(), depsFor(src, dst), t.TempDir(), "b", xm, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Decision != DecisionMove {
		t.Errorf("acts = %+v", acts)
	}
	if dst.mvs != 1 {
		t.Errorf("StateMv calls = %d, want 1", dst.mvs)
	}
	if src.pushes != 1 || dst.pushes != 1 {
		t.Errorf("pushes src=%d dst=%d, want 1 each", src.pushes, dst.pushes)
	}
}

func TestExecuteSkipAndAmbiguous(t *testing.T) {
	// dest already has it → skip, no mv/push.
	src := &fakeRunner{show: stateWith()}
	dst := &fakeRunner{show: stateWith("a.b")}
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "a.b", To: "a.b"}}}
	acts, err := Execute(context.Background(), depsFor(src, dst), t.TempDir(), "b", xm, false)
	if err != nil || len(acts) != 1 || acts[0].Decision != DecisionSkip {
		t.Fatalf("skip: acts=%+v err=%v", acts, err)
	}
	if dst.mvs != 0 || src.pushes != 0 || dst.pushes != 0 {
		t.Error("skip should not mv/push")
	}

	// both have it → ambiguous error, no mutation.
	src2 := &fakeRunner{show: stateWith("a.b")}
	dst2 := &fakeRunner{show: stateWith("a.b")}
	if _, err := Execute(context.Background(), depsFor(src2, dst2), t.TempDir(), "b", xm, false); err == nil {
		t.Error("both states have it → expected ambiguous error")
	}
	if dst2.mvs != 0 || src2.pushes != 0 {
		t.Error("ambiguous must abort before any mv/push")
	}
}

type fakeLocker struct {
	acquired []string
	released int
	failOn   string // stackDir suffix to fail Acquire on ("" = never)
}

func (l *fakeLocker) Acquire(_ context.Context, stackDir string) (func() error, error) {
	if l.failOn != "" && strings.HasSuffix(stackDir, l.failOn) {
		return nil, fmt.Errorf("locked: %s", stackDir)
	}
	l.acquired = append(l.acquired, stackDir)
	return func() error { l.released++; return nil }, nil
}

func TestExecuteAcquiresLockBeforePull(t *testing.T) {
	src := &fakeRunner{show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{show: stateWith()}
	lk := &fakeLocker{}
	deps := depsFor(src, dst)
	deps.Locker = lk
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}
	if _, err := Execute(context.Background(), deps, t.TempDir(), "b", xm, false); err != nil {
		t.Fatal(err)
	}
	if len(lk.acquired) != 2 {
		t.Errorf("acquired = %v, want 2 locks", lk.acquired)
	}
	if lk.released != 2 {
		t.Errorf("released = %d, want 2", lk.released)
	}
}

func TestExecuteFailsBeforePullWhenLocked(t *testing.T) {
	src := &fakeRunner{show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{show: stateWith()}
	lk := &fakeLocker{failOn: "/a"}
	deps := depsFor(src, dst)
	deps.Locker = lk
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}
	if _, err := Execute(context.Background(), deps, t.TempDir(), "b", xm, false); err == nil {
		t.Fatal("expected a lock error")
	}
	if src.pulls != 0 || dst.pulls != 0 {
		t.Errorf("state must NOT be pulled when locking failed (fail-before); pulls src=%d dst=%d", src.pulls, dst.pulls)
	}
}
