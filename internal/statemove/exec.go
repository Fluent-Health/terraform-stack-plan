package statemove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Runner is the subset of *tfexec.Terraform the executor needs (so tests can fake
// it without a binary).
type Runner interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	StatePull(ctx context.Context, opts ...tfexec.StatePullOption) (string, error)
	StatePush(ctx context.Context, path string, opts ...tfexec.StatePushCmdOption) error
	ShowStateFile(ctx context.Context, statePath string, opts ...tfexec.ShowOption) (*tfjson.State, error)
	StateMv(ctx context.Context, source, destination string, opts ...tfexec.StateMvCmdOption) error
}

// Locker is an optional pessimistic lock acquired before the pull→mv→push window
// so a concurrent terraform op on the same state fails to lock (rather than the
// move failing mid-flight). Acquire returns a release func; an error means the
// state is already locked and the move must NOT proceed.
type Locker interface {
	Acquire(ctx context.Context, stackDir string) (release func() error, err error)
}

// ExecDeps injects the terraform factory + a backup dir.
type ExecDeps struct {
	NewTF     func(workingDir string) (Runner, error) // builds a Runner rooted at a stack dir
	BackupDir string                                  // pre-move state backups (empty disables)
	Locker    Locker                                  // optional pessimistic lock around the move (nil = none)
}

// Action is what Execute did/would do for one pair.
type Action struct {
	From, To string
	Decision Decision
}

// Execute performs one xmove (dest = root/destStack, source = root/xm.SourceStack):
// pull both states → backup → decide per pair → (unless dryRun) state mv the
// to-move pairs on local files and push both (never --force). Fail-closed: any
// error aborts before pushing.
func Execute(ctx context.Context, deps ExecDeps, root, destStack string, xm XMove, dryRun bool) ([]Action, error) {
	srcDir := filepath.Join(root, filepath.FromSlash(xm.SourceStack))
	dstDir := filepath.Join(root, filepath.FromSlash(destStack))

	if deps.Locker != nil {
		relSrc, err := deps.Locker.Acquire(ctx, srcDir)
		if err != nil {
			return nil, fmt.Errorf("lock source %s: %w", xm.SourceStack, err)
		}
		defer func() { _ = relSrc() }()
		relDst, err := deps.Locker.Acquire(ctx, dstDir)
		if err != nil {
			return nil, fmt.Errorf("lock dest %s: %w", destStack, err)
		}
		defer func() { _ = relDst() }()
	}

	srcTF, err := deps.NewTF(srcDir)
	if err != nil {
		return nil, err
	}
	dstTF, err := deps.NewTF(dstDir)
	if err != nil {
		return nil, err
	}

	// Init both stacks so the GCS backend is configured before StatePull.
	// The CI changed-stack init loop only covers stacks that are "changed" in
	// the current PR; source and dest may both be absent when a follow-up PR
	// (e.g. a CI fix) triggers the apply with neither stack in the diff.
	if err := srcTF.Init(ctx); err != nil {
		return nil, fmt.Errorf("init source %s: %w", xm.SourceStack, err)
	}
	if err := dstTF.Init(ctx); err != nil {
		return nil, fmt.Errorf("init dest %s: %w", destStack, err)
	}

	srcState, err := srcTF.StatePull(ctx)
	if err != nil {
		return nil, fmt.Errorf("pull source state: %w", err)
	}
	dstState, err := dstTF.StatePull(ctx)
	if err != nil {
		return nil, fmt.Errorf("pull dest state: %w", err)
	}

	tmp, err := os.MkdirTemp("", "tfsp-xmove-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	srcFile := filepath.Join(tmp, "source.tfstate")
	dstFile := filepath.Join(tmp, "dest.tfstate")
	if err := backup(deps.BackupDir, xm.SourceStack, srcState); err != nil {
		return nil, err
	}
	if err := backup(deps.BackupDir, destStack, dstState); err != nil {
		return nil, err
	}

	srcAddrs, err := addressesOf(ctx, srcTF, srcState, srcFile)
	if err != nil {
		return nil, fmt.Errorf("read source state: %w", err)
	}
	dstAddrs, err := addressesOf(ctx, dstTF, dstState, dstFile)
	if err != nil {
		return nil, fmt.Errorf("read dest state: %w", err)
	}

	var actions []Action
	var toMove []Move
	for _, p := range xm.Pairs {
		d, err := decide(srcAddrs, dstAddrs, p.From, p.To)
		if err != nil {
			return nil, fmt.Errorf("move %s → %s: %w", p.From, p.To, err)
		}
		actions = append(actions, Action{From: p.From, To: p.To, Decision: d})
		if d == DecisionMove {
			toMove = append(toMove, p)
		}
	}
	if dryRun || len(toMove) == 0 {
		return actions, nil
	}

	for _, p := range toMove {
		if err := dstTF.StateMv(ctx, p.From, p.To, tfexec.State(srcFile), tfexec.StateOut(dstFile)); err != nil {
			return nil, fmt.Errorf("state mv %s → %s: %w", p.From, p.To, err)
		}
	}
	if err := srcTF.StatePush(ctx, srcFile, tfexec.Force(false)); err != nil {
		return nil, fmt.Errorf("push source state (concurrent change? never --force): %w", err)
	}
	if err := dstTF.StatePush(ctx, dstFile, tfexec.Force(false)); err != nil {
		return nil, fmt.Errorf("push dest state (concurrent change? never --force): %w", err)
	}
	return actions, nil
}

// addressesOf returns the resource addresses in a pulled state. An empty/blank
// state (e.g. a brand-new, never-applied stack) yields an empty set and is NOT
// written to disk — so `terraform state mv -state-out` creates the out file.
func addressesOf(ctx context.Context, tf Runner, state, file string) (map[string]bool, error) {
	if strings.TrimSpace(state) == "" {
		return map[string]bool{}, nil
	}
	if err := os.WriteFile(file, []byte(state), 0o600); err != nil {
		return nil, err
	}
	st, err := tf.ShowStateFile(ctx, file)
	if err != nil {
		return nil, err
	}
	return stateAddresses(st), nil
}

func backup(dir, stack, state string) error {
	if dir == "" {
		return nil
	}
	bd := filepath.Join(dir, filepath.FromSlash(stack))
	if err := os.MkdirAll(bd, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bd, fmt.Sprintf("%d.tfstate", time.Now().UnixNano())), []byte(state), 0o600)
}

// NewTerraform builds the real Runner for a stack dir.
func NewTerraform(execPath, workingDir string) (Runner, error) {
	return tfexec.NewTerraform(workingDir, execPath)
}
