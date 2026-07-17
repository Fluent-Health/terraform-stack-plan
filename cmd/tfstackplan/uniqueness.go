package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/uniqueness"
)

// runUniqueness executes the env_uniqueness lint: it discovers/loads the
// .tfstackplan.hcl policy, loads every Catalyst instance unit under --dir,
// evaluates them against the config's env_uniqueness{} block, renders the
// report (text or JSON), and returns the process exit code: 0 clean, 1
// unjustified violations or stale allows found, 2 usage/config/load error.
func runUniqueness(args []string) int {
	fs := flag.NewFlagSet("uniqueness", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo root to scan for Catalyst instance manifests")
	cfgPath := fs.String("config", "", "HCL config (default: auto-discover .tfstackplan.hcl under --dir)")
	format := fs.String("format", "text", "output format: text|json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	resolved := *cfgPath
	if resolved == "" {
		p, ok := config.Discover(*dir)
		if !ok {
			fmt.Fprintln(os.Stderr, "tfstackplan uniqueness: no .tfstackplan.hcl found under", *dir, "(pass --config)")
			return 2
		}
		resolved = p
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan uniqueness:", err)
		return 2
	}
	if cfg.EnvUniqueness == nil {
		fmt.Fprintln(os.Stderr, "tfstackplan uniqueness: no env_uniqueness block in", resolved)
		return 2
	}

	// Post-decode, EnvUniqueness.Source is always non-nil (decodeEnvUniqueness
	// synthesizes it when the source{} block is absent) and its Glob/paths are
	// already defaulted.
	units, err := uniqueness.LoadUnits(*dir, *cfg.EnvUniqueness.Source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan uniqueness:", err)
		return 2
	}

	rep, err := uniqueness.Evaluate(cfg.EnvUniqueness, units, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan uniqueness:", err)
		return 2
	}

	switch *format {
	case "text":
		renderUniquenessText(rep)
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan uniqueness:", err)
			return 2
		}
	default:
		fmt.Fprintf(os.Stderr, "tfstackplan uniqueness: --format must be text|json, got %q\n", *format)
		return 2
	}

	if len(rep.Unjustified) > 0 || len(rep.Stale) > 0 {
		return 1
	}
	return 0
}

// renderUniquenessText prints the human-readable report: an informational
// block for report-only findings (never blocking), then unjustified
// violations (unit/key/envs/kind), then stale allow rules (unit/key), and a
// final ok line when nothing is found in either of the latter two.
func renderUniquenessText(rep uniqueness.Report) {
	if len(rep.ReportOnly) > 0 {
		fmt.Println("Report-only findings (not blocking):")
		for _, v := range rep.ReportOnly {
			fmt.Printf("  %s: %s duplicated across %v (%s)\n", v.Unit, v.Key, v.Envs, v.Kind)
		}
	}

	if len(rep.Unjustified) > 0 {
		fmt.Println("Unjustified violations:")
		for _, v := range rep.Unjustified {
			fmt.Printf("  %s: %s across %v (%s)\n", v.Unit, v.Key, v.Envs, v.Kind)
		}
	}

	if len(rep.Stale) > 0 {
		fmt.Println("Stale allow rules (no longer match anything):")
		for _, a := range rep.Stale {
			fmt.Printf("  %s: %s\n", a.Unit, a.Key)
		}
	}

	if len(rep.Unjustified) == 0 && len(rep.Stale) == 0 {
		fmt.Println("tfstackplan uniqueness: ok — no unjustified violations or stale allows")
	}
}
