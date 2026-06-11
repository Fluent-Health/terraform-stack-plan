package statemove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Runner is the subset of *tfexec.Terraform the executor needs (so tests can fake
// it without a binary).
type Runner interface {
	StatePull(ctx context.Context, opts ...tfexec.StatePullOption) (string, error)
	StatePush(ctx context.Context, path string, opts ...tfexec.StatePushCmdOption) error
	ShowStateFile(ctx context.Context, statePath string, opts ...tfexec.ShowOption) (*tfjson.State, error)
	StateMv(ctx context.Context, source, destination string, opts ...tfexec.StateMvCmdOption) error
}

// ExecDeps injects the terraform factory + a backup dir.
type ExecDeps struct {
	NewTF     func(workingDir string) (Runner, error) // builds a Runner rooted at a stack dir
	BackupDir string                                  // pre-move state backups (empty disables)
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
	srcTF, err := deps.NewTF(srcDir)
	if err != nil {
		return nil, err
	}
	dstTF, err := deps.NewTF(dstDir)
	if err != nil {
		return nil, err
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
	if err := os.WriteFile(srcFile, []byte(srcState), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(dstFile, []byte(dstState), 0o600); err != nil {
		return nil, err
	}
	if err := backup(deps.BackupDir, xm.SourceStack, srcState); err != nil {
		return nil, err
	}
	if err := backup(deps.BackupDir, destStack, dstState); err != nil {
		return nil, err
	}

	srcShow, err := srcTF.ShowStateFile(ctx, srcFile)
	if err != nil {
		return nil, fmt.Errorf("show source state: %w", err)
	}
	dstShow, err := dstTF.ShowStateFile(ctx, dstFile)
	if err != nil {
		return nil, fmt.Errorf("show dest state: %w", err)
	}
	srcAddrs, dstAddrs := stateAddresses(srcShow), stateAddresses(dstShow)

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
