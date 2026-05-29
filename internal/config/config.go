// Package config loads and validates the HCL policy file (.tfstackplan.hcl),
// resolving classification presets+rules into a single ordered rule list while
// preserving source declaration order.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/Fluent-Health/terraform-stack-plan/internal/classify"
	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/presets"
)

// DefaultFilename is auto-discovered in the working directory.
const DefaultFilename = ".tfstackplan.hcl"

// Config is the parsed policy file.
type Config struct {
	Classification *Classification // nil when no classification block present
	Diff           DiffConfig
	Links          *LinksConfig // nil when no links block present
}

// Classification holds the resolved, ordered rules and the fallback class.
type Classification struct {
	Default model.Class
	Rules   []classify.Rule
}

// DiffConfig holds diff defaults and per-attribute overrides.
type DiffConfig struct {
	Detect            bool
	MaxAttributeLines int // 0 = no cap
	Overrides         []DiffOverride
}

// DiffOverride forces a differ for a (resource type, attribute) pair.
type DiffOverride struct {
	TypePattern *regexp.Regexp
	Attribute   string
	Differ      string
}

// Discover returns the config path if DefaultFilename exists under dir.
func Discover(dir string) (string, bool) {
	p := filepath.Join(dir, DefaultFilename)
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

// Load parses and validates the HCL file at path.
func Load(path string) (*Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %s", path, diags.Error())
	}
	cfg := &Config{Diff: DiffConfig{Detect: true}}
	body := f.Body.(*hclsyntax.Body)
	for _, blk := range body.Blocks {
		switch blk.Type {
		case "classification":
			cl, err := decodeClassification(blk)
			if err != nil {
				return nil, err
			}
			cfg.Classification = cl
		case "diff":
			d, err := decodeDiff(blk)
			if err != nil {
				return nil, err
			}
			cfg.Diff = d
		case "links":
			lc, err := decodeLinks(blk)
			if err != nil {
				return nil, err
			}
			cfg.Links = lc
		default:
			return nil, fmt.Errorf("%s: unknown top-level block %q", path, blk.Type)
		}
	}
	return cfg, nil
}

// --- classification ---

type defaultBlock struct {
	Name string `hcl:"name"`
	Icon string `hcl:"icon,optional"`
}

type ruleBody struct {
	Icon        string   `hcl:"icon,optional"`
	TypePattern string   `hcl:"resource_type_pattern,optional"`
	Actions     []string `hcl:"actions,optional"`
	MinCount    int      `hcl:"min_count,optional"`
}

type presetBody struct {
	Icon string `hcl:"icon,optional"`
}

func decodeClassification(blk *hclsyntax.Block) (*Classification, error) {
	cl := &Classification{Default: model.Class{Name: "safe"}}
	body := blk.Body

	if attr, ok := body.Attributes["default"]; ok {
		var s string
		if d := gohcl.DecodeExpression(attr.Expr, nil, &s); d.HasErrors() {
			return nil, fmt.Errorf("default: %s", d.Error())
		}
		cl.Default = model.Class{Name: s}
	}

	for _, b := range body.Blocks {
		switch b.Type {
		case "default":
			var db defaultBlock
			if d := gohcl.DecodeBody(b.Body, nil, &db); d.HasErrors() {
				return nil, fmt.Errorf("default block: %s", d.Error())
			}
			cl.Default = model.Class{Name: db.Name, Icon: db.Icon}
		case "preset":
			if len(b.Labels) != 1 {
				return nil, fmt.Errorf("preset block needs exactly one name label")
			}
			var pb presetBody
			if d := gohcl.DecodeBody(b.Body, nil, &pb); d.HasErrors() {
				return nil, fmt.Errorf("preset %q: %s", b.Labels[0], d.Error())
			}
			rule, ok := presets.Get(b.Labels[0], pb.Icon)
			if !ok {
				return nil, fmt.Errorf("unknown preset %q (available: %v)", b.Labels[0], presets.Names)
			}
			cl.Rules = append(cl.Rules, rule)
		case "rule":
			if len(b.Labels) != 1 {
				return nil, fmt.Errorf("rule block needs exactly one name label")
			}
			var rb ruleBody
			if d := gohcl.DecodeBody(b.Body, nil, &rb); d.HasErrors() {
				return nil, fmt.Errorf("rule %q: %s", b.Labels[0], d.Error())
			}
			rule := classify.Rule{Name: b.Labels[0], Icon: rb.Icon, Actions: rb.Actions, MinCount: rb.MinCount}
			if rb.TypePattern != "" {
				re, err := regexp.Compile(rb.TypePattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q: bad resource_type_pattern: %w", b.Labels[0], err)
				}
				rule.TypePattern = re
			}
			cl.Rules = append(cl.Rules, rule)
		default:
			return nil, fmt.Errorf("classification: unknown block %q", b.Type)
		}
	}
	return cl, nil
}

// --- diff ---

func decodeDiff(blk *hclsyntax.Block) (DiffConfig, error) {
	d := DiffConfig{Detect: true}
	body := blk.Body
	if a, ok := body.Attributes["detect"]; ok {
		var v bool
		if dg := gohcl.DecodeExpression(a.Expr, nil, &v); dg.HasErrors() {
			return d, fmt.Errorf("diff.detect: %s", dg.Error())
		}
		d.Detect = v
	}
	if a, ok := body.Attributes["max_attribute_lines"]; ok {
		var v int
		if dg := gohcl.DecodeExpression(a.Expr, nil, &v); dg.HasErrors() {
			return d, fmt.Errorf("diff.max_attribute_lines: %s", dg.Error())
		}
		d.MaxAttributeLines = v
	}
	for _, b := range body.Blocks {
		if b.Type != "rule" {
			return d, fmt.Errorf("diff: unknown block %q", b.Type)
		}
		var rb diffRuleBody
		if dg := gohcl.DecodeBody(b.Body, nil, &rb); dg.HasErrors() {
			return d, fmt.Errorf("diff rule: %s", dg.Error())
		}
		ov := DiffOverride{Attribute: rb.Attribute, Differ: rb.Differ}
		if rb.TypePattern != "" {
			re, err := regexp.Compile(rb.TypePattern)
			if err != nil {
				return d, fmt.Errorf("diff rule: bad resource_type_pattern: %w", err)
			}
			ov.TypePattern = re
		}
		d.Overrides = append(d.Overrides, ov)
	}
	return d, nil
}

type diffRuleBody struct {
	TypePattern string `hcl:"resource_type_pattern,optional"`
	Attribute   string `hcl:"attribute,optional"`
	Differ      string `hcl:"differ,optional"`
}

// --- links ---

// LinksConfig holds URL templates for header / stack / resource links.
type LinksConfig struct {
	Resource string
	Stack    string
	Header   []HeaderLink
}

// HeaderLink is one templated report-header link.
type HeaderLink struct {
	Label string
	URL   string
}

type headerBlock struct {
	Label string `hcl:"label"`
	URL   string `hcl:"url"`
}

func decodeLinks(blk *hclsyntax.Block) (*LinksConfig, error) {
	lc := &LinksConfig{}
	body := blk.Body
	for name, target := range map[string]*string{"resource": &lc.Resource, "stack": &lc.Stack} {
		if a, ok := body.Attributes[name]; ok {
			var s string
			if d := gohcl.DecodeExpression(a.Expr, nil, &s); d.HasErrors() {
				return nil, fmt.Errorf("links.%s: %s", name, d.Error())
			}
			*target = s
		}
	}
	for _, b := range body.Blocks {
		if b.Type != "header" {
			return nil, fmt.Errorf("links: unknown block %q", b.Type)
		}
		var hb headerBlock
		if d := gohcl.DecodeBody(b.Body, nil, &hb); d.HasErrors() {
			return nil, fmt.Errorf("links.header: %s", d.Error())
		}
		lc.Header = append(lc.Header, HeaderLink{Label: hb.Label, URL: hb.URL})
	}
	return lc, nil
}

// Resolve returns the differ kind ("" = auto) for a resource type + attribute.
func (d DiffConfig) Resolve(resourceType, attr string) string {
	for _, ov := range d.Overrides {
		if ov.TypePattern != nil && !ov.TypePattern.MatchString(resourceType) {
			continue
		}
		if ov.Attribute != "" {
			if ok, _ := filepath.Match(ov.Attribute, attr); !ok {
				continue
			}
		}
		return ov.Differ
	}
	return ""
}
