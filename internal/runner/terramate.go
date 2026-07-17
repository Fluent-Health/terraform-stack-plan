package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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

// WhyChanged returns the change reasons for each changed stack relative to base
// (empty uses terramate's configured default), using terramate list --changed --why.
func (t *Terramate) WhyChanged(ctx context.Context, base string) ([]events.ChangeReason, error) {
	args := []string{"list", "--changed", "--why"}
	if base != "" {
		args = append(args, "-B", base)
	}
	out, err := t.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	rawLines := lines(out)
	reasons := make([]events.ChangeReason, len(rawLines))
	for i, line := range rawLines {
		reasons[i] = ParseLine(line)
	}
	return reasons, nil
}

// RunGraph returns the stack dependency edges (From must run before To), derived
// from `terramate experimental run-graph -l stack.dir`.
func (t *Terramate) RunGraph(ctx context.Context) ([]events.Edge, error) {
	out, err := t.output(ctx, "experimental", "run-graph", "-l", "stack.dir")
	if err != nil {
		return nil, err
	}
	return parseRunGraph(string(out)), nil
}

// ScriptRunOptions controls `terramate script run`.
type ScriptRunOptions struct {
	Script   string // the terramate script name (e.g. "plan")
	Changed  bool   // --changed: only changed stacks
	Parallel int    // --parallel N (0 = terramate default)
	Base     string // -B <ref> for change detection
}

// ScriptRun runs a terramate script across (changed) stacks, streaming combined
// output to w. It returns terramate's exit error (so a failed stack fails the run).
func (t *Terramate) ScriptRun(ctx context.Context, w io.Writer, o ScriptRunOptions) error {
	args := []string{"script", "run"}
	if o.Changed {
		args = append(args, "--changed")
	}
	if o.Parallel > 0 {
		args = append(args, "--parallel", strconv.Itoa(o.Parallel))
	}
	if o.Base != "" {
		args = append(args, "-B", o.Base)
	}
	args = append(args, o.Script)
	cmd := exec.CommandContext(ctx, t.bin(), args...)
	cmd.Dir = t.Dir
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}
