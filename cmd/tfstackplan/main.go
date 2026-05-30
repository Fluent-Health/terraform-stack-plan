// Command tfstackplan merges many Terraform plan.json files into one
// marker-keyed markdown report for a PR comment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/classify"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/differ"
	"github.com/Fluent-Health/terraform-stack-plan/internal/fit"
	"github.com/Fluent-Health/terraform-stack-plan/internal/links"
	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plandir"
	"github.com/Fluent-Health/terraform-stack-plan/internal/render"
	"github.com/Fluent-Health/terraform-stack-plan/internal/source"
)

const defaultMaxBytes = 60000

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

// repeatedFlag is a generic repeatable string flag value.
type repeatedFlag []string

func (s *repeatedFlag) String() string { return fmt.Sprint(*s) }
func (s *repeatedFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type opts struct {
	plansDir  string
	title     string
	marker    string
	config    string
	maxBytes  int
	output    string
	classJSON string
	details   string
	repoRoot  string
	linkVars  []string
}

func main() {
	var o opts
	flag.StringVar(&o.plansDir, "plans-dir", "", "directory of per-stack plans (each <stack>/tfplan.json)")
	flag.StringVar(&o.title, "title", "Terraform plan", "report title")
	flag.StringVar(&o.marker, "marker", "tfstackplan", "HTML-comment marker for CI upsert")
	flag.StringVar(&o.config, "config", "", "HCL policy file (default: auto-discover .tfstackplan.hcl)")
	flag.IntVar(&o.maxBytes, "max-bytes", defaultMaxBytes, "document byte budget (0 disables)")
	flag.StringVar(&o.output, "output", "-", "output file ('-' = stdout)")
	flag.StringVar(&o.classJSON, "emit-classification-json", "", "write computed classes as JSON")
	flag.StringVar(&o.details, "details", "closed", "details disclosure: auto|open|closed")
	flag.StringVar(&o.repoRoot, "repo-root", ".", "repo root for computing link file paths")
	var lv repeatedFlag
	flag.Var(&lv, "link-var", "link template variable as key=value (repeatable); sha=<sha> also derives sha_short")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("tfstackplan", version)
		return
	}
	o.linkVars = lv

	out, fits, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan:", err)
		os.Exit(1)
	}
	if o.output == "-" || o.output == "" {
		fmt.Print(out)
	} else if err := os.WriteFile(o.output, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan:", err)
		os.Exit(1)
	}
	if !fits {
		fmt.Fprintln(os.Stderr, "tfstackplan: warning: report exceeds --max-bytes even after full reduction")
		os.Exit(2)
	}
}

// run executes the whole pipeline and returns the markdown document and whether
// it fits the configured byte budget.
func run(o opts) (string, bool, error) {
	if o.plansDir == "" {
		return "", false, fmt.Errorf("no input: pass --plans-dir")
	}
	refs, err := plandir.Scan(o.plansDir)
	if err != nil {
		return "", false, err
	}

	var cfg *config.Config
	cfgPath := o.config
	if cfgPath == "" {
		if p, ok := config.Discover("."); ok {
			cfgPath = p
		}
	}
	if cfgPath != "" {
		c, err := config.Load(cfgPath)
		if err != nil {
			return "", false, err
		}
		cfg = c
	} else {
		cfg = &config.Config{Diff: config.DiffConfig{Detect: true}}
	}
	classified := cfg.Classification != nil

	report := model.Report{Title: o.title, Marker: o.marker, Classified: classified}
	if classified {
		report.Default = cfg.Classification.Default
	}
	base := baseVars(o.linkVars)
	if cfg.Links != nil {
		for _, l := range cfg.Links.Header {
			if url := links.Resolve(l.URL, base); url != "" {
				report.HeaderLinks = append(report.HeaderLinks, model.Link{Label: links.Resolve(l.Label, base), URL: url})
			}
		}
	}
	doc := sidecarDoc{Stacks: map[string]stackEntry{}}
	var allCats [][]classify.Category
	for _, ref := range refs {
		data, err := os.ReadFile(ref.Plan)
		if err != nil {
			return "", false, fmt.Errorf("stack %q: %w", ref.Name, err)
		}
		raw, err := plan.Parse(ref.Name, data)
		if err != nil {
			return "", false, err
		}
		st := model.Stack{Name: ref.Name, Counts: raw.Counts}

		stackDir := filepath.Join(o.repoRoot, filepath.FromSlash(ref.Name))
		stackVars := mergeVars(base, map[string]string{"stack": ref.Name, "stack_dir": relSlash(o.repoRoot, stackDir)})
		var srcIdx *source.Index
		if cfg.Links != nil {
			st.URL = links.Resolve(cfg.Links.Stack, stackVars)
			if cfg.Links.Resource != "" {
				srcIdx = source.Build(stackDir, o.repoRoot)
			}
		}

		if classified {
			cats := classify.Classify(raw, cfg.Classification.Rules)
			st.Categories = toClasses(cats)
			allCats = append(allCats, cats)
			doc.Stacks[ref.Name] = stackEntry{Categories: toEntries(cats)}
		}

		for _, rc := range raw.Changes {
			ch := model.Change{
				Address:         rc.Address,
				Type:            rc.Type,
				Action:          rc.Action,
				Moved:           rc.Moved,
				PreviousAddress: rc.PreviousAddress,
				Imported:        rc.Imported,
				ImportID:        rc.ImportID,
			}
			for _, ra := range rc.Attrs {
				kind := cfg.Diff.Resolve(rc.Type, ra.Name)
				f := differ.Diff(differ.Input{
					ResourceType: rc.Type,
					Attr:         ra.Name,
					Before:       ra.Before,
					After:        ra.After,
					Sensitive:    ra.Sensitive,
					Unknown:      ra.Unknown,
					ForceDiffer:  kind,
					MaxLines:     cfg.Diff.MaxAttributeLines,
					NoDetect:     !cfg.Diff.Detect,
				})
				ch.Fields = append(ch.Fields, f)
			}
			if cfg.Links != nil {
				ch.URL = st.URL // fall back to the stack link
				if srcIdx != nil {
					if loc, ok := srcIdx.Lookup(rc.ModuleAddress, rc.Type, rc.Name); ok {
						rv := mergeVars(stackVars, map[string]string{
							"file": loc.File, "line": fmt.Sprintf("%d", loc.Line),
							"type": rc.Type, "name": rc.Name, "address": rc.Address, "module": rc.ModuleAddress,
						})
						if u := links.Resolve(cfg.Links.Resource, rv); u != "" {
							ch.URL = u
						}
					}
				}
			}
			st.Changes = append(st.Changes, ch)
		}
		report.Stacks = append(report.Stacks, st)
	}

	switch o.details {
	case "open":
		report.DetailsOpen = true
	case "", "closed":
		report.DetailsOpen = false
	case "auto":
		changed := 0
		for _, s := range report.Stacks {
			if s.Counts.AnyChange() {
				changed++
			}
		}
		report.DetailsOpen = changed == 1
	default:
		return "", false, fmt.Errorf("--details must be auto|open|closed, got %q", o.details)
	}

	fits := fit.Fit(&report, o.maxBytes)

	if o.classJSON != "" && classified {
		doc.Summary.Categories = toEntries(classify.Summarize(allCats, cfg.Classification.Rules))
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", false, err
		}
		if err := os.WriteFile(o.classJSON, data, 0o644); err != nil {
			return "", false, err
		}
	}

	return render.Render(report), fits, nil
}

type categoryEntry struct {
	Category   string              `json:"category"`
	Icon       *string             `json:"icon"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

type stackEntry struct {
	Categories []categoryEntry `json:"categories"`
}

type sidecarDoc struct {
	Stacks  map[string]stackEntry `json:"stacks"`
	Summary struct {
		Categories []categoryEntry `json:"categories"`
	} `json:"summary"`
}

// toEntries maps classify categories to their JSON form. Always returns a
// non-nil slice so a category-less stack marshals as [] rather than null.
func toEntries(cats []classify.Category) []categoryEntry {
	out := make([]categoryEntry, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryEntry{Category: c.Name, Icon: nilable(c.Icon), Attributes: c.Attributes})
	}
	return out
}

// toClasses maps classify categories to render-model classes (name+icon only).
func toClasses(cats []classify.Category) []model.Class {
	out := make([]model.Class, len(cats))
	for i, c := range cats {
		out[i] = model.Class{Name: c.Name, Icon: c.Icon}
	}
	return out
}

func nilable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// baseVars parses run-level link variables from key=value pairs and derives
// sha_short (first 7 chars of sha) when present.
func baseVars(pairs []string) map[string]string {
	v := map[string]string{}
	for _, p := range pairs {
		if i := strings.IndexByte(p, '='); i > 0 {
			v[p[:i]] = p[i+1:]
		}
	}
	if sha := v["sha"]; len(sha) >= 7 {
		v["sha_short"] = sha[:7]
	}
	return v
}

func mergeVars(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// relSlash returns target relative to root as a forward-slash path, or "" on error.
func relSlash(root, target string) string {
	ra, e1 := filepath.Abs(root)
	ta, e2 := filepath.Abs(target)
	if e1 != nil || e2 != nil {
		return ""
	}
	rel, err := filepath.Rel(ra, ta)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
