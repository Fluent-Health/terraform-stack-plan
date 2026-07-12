package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type Component struct {
	ID      string   `json:"id"`
	Stacks  []string `json:"stacks"`
	Watches []string `json:"watches"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // "watch" or "dependency"
}

type Catalog struct {
	Components []Component `json:"components"`
	Edges      []Edge      `json:"edges"`
}

func Build(dir string) (Catalog, error) {
	ctx := context.Background()
	tm := &runner.Terramate{Dir: dir}
	stacks, err := tm.List(ctx)
	if err != nil {
		return Catalog{}, fmt.Errorf("list stacks: %w", err)
	}

	// Group stacks by component (using path segmentation: e.g. stacks/nonprod/am/fh-dev-svc -> nonprod/am)
	groups := map[string][]string{}
	watchesMap := map[string][]string{}

	for _, s := range stacks {
		parts := strings.Split(s, "/")
		comp := s
		if len(parts) > 3 && parts[0] == "stacks" {
			comp = strings.Join(parts[1:3], "/")
		} else if len(parts) > 2 {
			comp = strings.Join(parts[:2], "/")
		}
		groups[comp] = append(groups[comp], s)

		// Parse HCL watch expressions for this stack
		stackDir := filepath.Join(dir, filepath.FromSlash(s))
		for _, file := range []string{"stack.tm.hcl", "terramate.tm.hcl", ".tm.hcl"} {
			filePath := filepath.Join(stackDir, file)
			if w, cerr := ParseWatches(filePath); cerr == nil && len(w) > 0 {
				watchesMap[comp] = append(watchesMap[comp], w...)
				break
			}
		}
	}

	var components []Component
	for id, sList := range groups {
		watches := dedup(watchesMap[id])
		components = append(components, Component{
			ID:      id,
			Stacks:  sList,
			Watches: watches,
		})
	}

	// Fetch dependency edges from RunGraph
	rawEdges, _ := tm.RunGraph(ctx)
	edgesMap := map[string]bool{}
	var edges []Edge

	// 1. Build watch-causality edges
	for _, comp := range components {
		for _, w := range comp.Watches {
			for _, other := range components {
				if other.ID == comp.ID {
					continue
				}
				otherName := filepath.Base(other.ID)
				if strings.Contains(w, otherName) {
					key := fmt.Sprintf("%s:%s:watch", other.ID, comp.ID)
					if !edgesMap[key] {
						edgesMap[key] = true
						edges = append(edges, Edge{From: other.ID, To: comp.ID, Kind: "watch"})
					}
				}
			}
		}
	}

	// 2. Aggregate stack dependencies to component-level edges
	for _, e := range rawEdges {
		fromComp := getComponent(e.From)
		toComp := getComponent(e.To)
		if fromComp == "" || toComp == "" || fromComp == toComp {
			continue
		}
		key := fmt.Sprintf("%s:%s:dependency", fromComp, toComp)
		if !edgesMap[key] {
			edgesMap[key] = true
			edges = append(edges, Edge{From: fromComp, To: toComp, Kind: "dependency"})
		}
	}

	return Catalog{
		Components: components,
		Edges:      edges,
	}, nil
}

func getComponent(stack string) string {
	parts := strings.Split(stack, "/")
	if len(parts) > 3 && parts[0] == "stacks" {
		return strings.Join(parts[1:3], "/")
	} else if len(parts) > 2 {
		return strings.Join(parts[:2], "/")
	}
	return stack
}

func dedup(list []string) []string {
	m := map[string]bool{}
	var out []string
	for _, v := range list {
		if !m[v] {
			m[v] = true
			out = append(out, v)
		}
	}
	return out
}

func ParseWatches(filePath string) ([]string, error) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(src, filePath, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, diags
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("invalid body")
	}
	for _, blk := range body.Blocks {
		if blk.Type != "stack" {
			continue
		}
		for _, attr := range blk.Body.Attributes {
			if attr.Name != "watch" {
				continue
			}
			tuple, ok := attr.Expr.(*hclsyntax.TupleConsExpr)
			if !ok {
				continue
			}
			var watches []string
			for _, expr := range tuple.Exprs {
				lit, ok := expr.(*hclsyntax.TemplateExpr)
				if ok && len(lit.Parts) == 1 {
					if txt, ok := lit.Parts[0].(*hclsyntax.LiteralValueExpr); ok {
						watches = append(watches, txt.Val.AsString())
					}
				} else if litVal, ok := expr.(*hclsyntax.LiteralValueExpr); ok {
					watches = append(watches, litVal.Val.AsString())
				}
			}
			return watches, nil
		}
	}
	return nil, nil
}
