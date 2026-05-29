// Command tfstackplan merges many Terraform plan.json files into one
// marker-keyed markdown report for a PR comment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/classify"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/differ"
	"github.com/Fluent-Health/terraform-stack-plan/internal/fit"
	"github.com/Fluent-Health/terraform-stack-plan/internal/manifest"
	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
	"github.com/Fluent-Health/terraform-stack-plan/internal/render"
)

const defaultMaxBytes = 60000

type stackFlags []string

func (s *stackFlags) String() string { return fmt.Sprint(*s) }
func (s *stackFlags) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type opts struct {
	manifestPath string
	stacks       []string
	title        string
	marker       string
	config       string
	maxBytes     int
	output       string
	classJSON    string
	details      string
}

func main() {
	var o opts
	var sf stackFlags
	flag.StringVar(&o.manifestPath, "manifest", "", "manifest file (YAML/JSON)")
	flag.Var(&sf, "stack", "stack as NAME:PATH (repeatable)")
	flag.StringVar(&o.title, "title", "Terraform plan", "report title")
	flag.StringVar(&o.marker, "marker", "tfstackplan", "HTML-comment marker for CI upsert")
	flag.StringVar(&o.config, "config", "", "HCL policy file (default: auto-discover .tfstackplan.hcl)")
	flag.IntVar(&o.maxBytes, "max-bytes", defaultMaxBytes, "document byte budget (0 disables)")
	flag.StringVar(&o.output, "output", "-", "output file ('-' = stdout)")
	flag.StringVar(&o.classJSON, "emit-classification-json", "", "write computed classes as JSON")
	flag.StringVar(&o.details, "details", "closed", "details disclosure: auto|open|closed")
	flag.Parse()
	o.stacks = sf

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
	var refs []manifest.StackRef
	switch {
	case o.manifestPath != "" && len(o.stacks) > 0:
		return "", false, fmt.Errorf("--manifest and --stack are mutually exclusive")
	case o.manifestPath != "":
		m, err := manifest.Load(o.manifestPath)
		if err != nil {
			return "", false, err
		}
		refs = m.Stacks
		if o.title == "Terraform plan" && m.Title != "" {
			o.title = m.Title
		}
		if o.marker == "tfstackplan" && m.Marker != "" {
			o.marker = m.Marker
		}
	case len(o.stacks) > 0:
		r, err := manifest.ParseStackFlags(o.stacks)
		if err != nil {
			return "", false, err
		}
		refs = r
	default:
		return "", false, fmt.Errorf("no stacks: pass --manifest or --stack")
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
	sidecar := map[string]classEntry{}
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

		if classified {
			cl := classify.Classify(raw, cfg.Classification.Rules, cfg.Classification.Default)
			st.Class = &cl
			sidecar[ref.Name] = classEntry{Class: cl.Name, Icon: nilable(cl.Icon)}
		}

		for _, rc := range raw.Changes {
			ch := model.Change{Address: rc.Address, Type: rc.Type, Action: rc.Action}
			for _, ra := range rc.Attrs {
				kind := cfg.Diff.Resolve(rc.Type, ra.Name)
				maxLines := cfg.Diff.MaxAttributeLines
				ad := differ.Diff(differ.Input{
					ResourceType: rc.Type,
					Attr:         ra.Name,
					Before:       ra.Before,
					After:        ra.After,
					Sensitive:    ra.Sensitive,
					Unknown:      ra.Unknown,
					ForceDiffer:  kind,
					MaxLines:     maxLines,
					NoDetect:     !cfg.Diff.Detect,
				})
				ch.Attrs = append(ch.Attrs, ad)
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
		data, err := json.MarshalIndent(sidecar, "", "  ")
		if err != nil {
			return "", false, err
		}
		if err := os.WriteFile(o.classJSON, data, 0o644); err != nil {
			return "", false, err
		}
	}

	return render.Render(report), fits, nil
}

type classEntry struct {
	Class string  `json:"class"`
	Icon  *string `json:"icon"`
}

func nilable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
