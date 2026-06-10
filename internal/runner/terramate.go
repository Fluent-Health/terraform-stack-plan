package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Terramate shells out to the terramate binary. Dir is the project root: it is
// set as the process working directory (so asdf resolves the project's
// .tool-versions and terramate operates on that tree) — no --chdir is used.
type Terramate struct {
	Bin string // terramate binary; defaults to "terramate"
	Dir string // project root (process cwd for every invocation)
}

func (t *Terramate) bin() string {
	if t.Bin != "" {
		return t.Bin
	}
	return "terramate"
}

// output runs terramate with the given args and returns stdout, or an error
// including stderr.
func (t *Terramate) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.bin(), args...)
	cmd.Dir = t.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("terramate %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// lines splits command output into trimmed, non-empty lines.
func lines(b []byte) []string {
	out := []string{}
	for _, ln := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// List returns every stack path in the project.
func (t *Terramate) List(ctx context.Context) ([]string, error) {
	out, err := t.output(ctx, "list")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// ChangedStacks returns the stacks changed relative to base (empty uses
// terramate's configured default).
func (t *Terramate) ChangedStacks(ctx context.Context, base string) ([]string, error) {
	args := []string{"list", "--changed"}
	if base != "" {
		args = append(args, "-B", base)
	}
	out, err := t.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}
