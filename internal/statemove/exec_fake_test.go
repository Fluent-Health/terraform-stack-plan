package statemove

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

type fakeRunner struct {
	dir       string
	stateJSON string        // returned by StatePull
	show      *tfjson.State // returned by ShowStateFile
	inits     int
	mvs       int
	pushes    int
	pulls     int
	pushForce []bool            // the --force flag captured per StatePush call
	pushErr   func(n int) error // if set, StatePush returns pushErr(pushCount) (n is 1-based)
}

func (f *fakeRunner) Init(context.Context, ...tfexec.InitOption) error {
	f.inits++
	return nil
}

func (f *fakeRunner) StatePull(context.Context, ...tfexec.StatePullOption) (string, error) {
	f.pulls++
	return f.stateJSON, nil
}
func (f *fakeRunner) ShowStateFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.State, error) {
	if f.show == nil {
		return nil, fmt.Errorf("ShowStateFile called on empty state")
	}
	return f.show, nil
}
func (f *fakeRunner) StateMv(context.Context, string, string, ...tfexec.StateMvCmdOption) error {
	f.mvs++
	return nil
}
func (f *fakeRunner) StatePush(_ context.Context, _ string, opts ...tfexec.StatePushCmdOption) error {
	f.pushes++
	// Capture the --force flag per push (forward pushes use Force(false); the
	// recovery rollback uses Force(true)). tfexec.Force returns the exported
	// *ForceOption with an unexported `force` field; read it via reflect.
	force := false
	for _, o := range opts {
		if fo, ok := o.(*tfexec.ForceOption); ok {
			force = reflect.ValueOf(fo).Elem().FieldByName("force").Bool()
		}
	}
	f.pushForce = append(f.pushForce, force)
	if f.pushErr != nil {
		return f.pushErr(f.pushes)
	}
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
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{stateJSON: "non-empty", show: stateWith()}
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

func TestExecuteEmptyDestState(t *testing.T) {
	// Source has the resource; dest is a brand-new stack → empty pull, no Show.
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{stateJSON: "", show: nil} // empty pull; ShowStateFile must not be called
	deps := depsFor(src, dst)
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}
	acts, err := Execute(context.Background(), deps, t.TempDir(), "b", xm, false)
	if err != nil {
		t.Fatalf("execute into empty dest: %v", err)
	}
	if len(acts) != 1 || acts[0].Decision != DecisionMove {
		t.Fatalf("actions = %+v, want one DecisionMove into the empty dest", acts)
	}
	if dst.mvs != 1 || dst.pushes != 1 || src.pushes != 1 {
		t.Errorf("expected 1 mv on the mv-runner + push of both; got dst.mvs=%d dst.pushes=%d src.pushes=%d", dst.mvs, dst.pushes, src.pushes)
	}
}

func TestExecuteSkipAndAmbiguous(t *testing.T) {
	// dest already has it → skip, no mv/push.
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith()}
	dst := &fakeRunner{stateJSON: "non-empty", show: stateWith("a.b")}
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "a.b", To: "a.b"}}}
	acts, err := Execute(context.Background(), depsFor(src, dst), t.TempDir(), "b", xm, false)
	if err != nil || len(acts) != 1 || acts[0].Decision != DecisionSkip {
		t.Fatalf("skip: acts=%+v err=%v", acts, err)
	}
	if dst.mvs != 0 || src.pushes != 0 || dst.pushes != 0 {
		t.Error("skip should not mv/push")
	}

	// both have it → ambiguous error, no mutation.
	src2 := &fakeRunner{stateJSON: "non-empty", show: stateWith("a.b")}
	dst2 := &fakeRunner{stateJSON: "non-empty", show: stateWith("a.b")}
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
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{stateJSON: "non-empty", show: stateWith()}
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

// TestExecuteMovesWholeModule proves a manifest may name a whole module: Execute
// fans the single module pair out to the children present in the source state and
// state-mv's each (the original problem — a bare module address never matched
// decide's exact lookup).
func TestExecuteMovesWholeModule(t *testing.T) {
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith(
		"module.a[0].google_x.one",
		"module.a[0].google_y.two[\"k\"]",
		"module.a[0].module.sub.google_z.three",
	)}
	dst := &fakeRunner{stateJSON: "", show: stateWith()} // empty dest
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "module.a[0]", To: "module.b"}}}
	actions, err := Execute(context.Background(), depsFor(src, dst), ".", "b", xm, false)
	if err != nil {
		t.Fatalf("Execute whole-module: %v", err)
	}
	if len(actions) != 3 {
		t.Fatalf("want 3 expanded actions, got %d: %+v", len(actions), actions)
	}
	for _, a := range actions {
		if a.Decision != DecisionMove {
			t.Errorf("action %+v: want Move", a)
		}
	}
	if dst.mvs != 3 {
		t.Errorf("want 3 state mv calls, got %d", dst.mvs)
	}
}

func TestExecuteRollsBackSourceOnDestPushFailure(t *testing.T) {
	// src push succeeds, dest push fails → the source must be re-pushed (rolled
	// back to its pre-move state) so the moved resources are not lost.
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith("aws_s3_bucket.x")}
	dst := &fakeRunner{stateJSON: "non-empty", show: stateWith(),
		pushErr: func(int) error { return fmt.Errorf("dest push boom") }}
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}

	_, err := Execute(context.Background(), depsFor(src, dst), t.TempDir(), "b", xm, false)
	if err == nil {
		t.Fatal("want error on dest push failure")
	}
	if !strings.Contains(err.Error(), "rolled") {
		t.Errorf("error should say the source was rolled back, got: %v", err)
	}
	if src.pushes != 2 {
		t.Errorf("source pushes = %d, want 2 (forward + rollback)", src.pushes)
	}
	if dst.pushes != 1 {
		t.Errorf("dest pushes = %d, want 1", dst.pushes)
	}
	// The forward source push is Force(false); the recovery rollback is Force(true).
	if want := []bool{false, true}; !reflect.DeepEqual(src.pushForce, want) {
		t.Errorf("source push --force = %v, want %v (forward false, rollback true)", src.pushForce, want)
	}
}

func TestExecuteLoudErrorWhenRollbackAlsoFails(t *testing.T) {
	// dest push fails AND the source rollback push also fails → loud error naming
	// the backups for manual restore.
	src := &fakeRunner{stateJSON: "non-empty", show: stateWith("aws_s3_bucket.x"),
		pushErr: func(n int) error {
			if n == 2 { // the rollback push
				return fmt.Errorf("rollback push boom")
			}
			return nil
		}}
	dst := &fakeRunner{stateJSON: "non-empty", show: stateWith(),
		pushErr: func(int) error { return fmt.Errorf("dest push boom") }}
	xm := XMove{SourceStack: "a", Pairs: []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}}}

	deps := depsFor(src, dst)
	deps.BackupDir = t.TempDir() // writable, so backup() succeeds and we reach the push phase; exercises the path-naming branch
	_, err := Execute(context.Background(), deps, t.TempDir(), "b", xm, false)
	if err == nil || !strings.Contains(err.Error(), "orphaned") {
		t.Fatalf("want a loud 'orphaned — restore from backups' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), deps.BackupDir) {
		t.Errorf("loud error must name the backup dir %q for manual restore, got: %v", deps.BackupDir, err)
	}
	if src.pushes != 2 {
		t.Errorf("source pushes = %d, want 2 (forward + failed rollback)", src.pushes)
	}
	if dst.pushes != 1 {
		t.Errorf("dest pushes = %d, want 1", dst.pushes)
	}
}
