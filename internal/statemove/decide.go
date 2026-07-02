package statemove

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// AddressSet maps resource addresses to their corresponding ProviderName.
type AddressSet map[string]string

// DestProviders represents a set of destination providers.
type DestProviders map[string]bool

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Diagnostic struct {
	Code     string
	Severity Severity
	Message  string
}

// expandPairs resolves each declared move pair against the live source/dest state
// addresses. A module-/prefix-level pair (e.g. module.x[0] -> module.y) fans out
// to the concrete per-resource pairs it covers (module.x[0].r -> module.y.r),
// mirroring CrossStackPairs (which fans out against a plan) — so a manifest may
// name a whole module and the move still works. An exact resource/instance pair
// resolves to itself. An already-moved pair (nothing under `from` in the source,
// children under `to` in the dest) is re-keyed from `to`'s children so decide()
// returns Skip (idempotent re-run). A pair matching neither side is kept verbatim
// so decide() fails closed. matches() is the same exact-or-child ("."/"[" boundary)
// relation classify uses, keeping plan-time and apply-time semantics aligned.
func expandPairs(srcAddrs, dstAddrs AddressSet, pairs []Move) []Move {
	var out []Move
	for _, p := range pairs {
		var src, dst []string
		for a := range srcAddrs {
			if matches(a, p.From) {
				// Longest-prefix-wins: if a more specific (longer) From entry exists in pairs,
				// do not expand this resource under the shorter wildcard.
				moreSpecific := false
				for _, other := range pairs {
					if other.From != p.From && len(other.From) > len(p.From) && matches(a, other.From) {
						moreSpecific = true
						break
					}
				}
				if !moreSpecific {
					src = append(src, a)
				}
			}
		}
		for a := range dstAddrs {
			if matches(a, p.To) {
				// Longest-prefix-wins: if a more specific (longer) To entry exists in pairs,
				// do not expand this resource under the shorter wildcard.
				moreSpecific := false
				for _, other := range pairs {
					if other.To != p.To && len(other.To) > len(p.To) && matches(a, other.To) {
						moreSpecific = true
						break
					}
				}
				if !moreSpecific {
					dst = append(dst, a)
				}
			}
		}
		switch {
		case len(src) > 0:
			sort.Strings(src)
			for _, s := range src {
				out = append(out, Move{From: s, To: p.To + s[len(p.From):]})
			}
		case len(dst) > 0:
			sort.Strings(dst)
			for _, d := range dst {
				out = append(out, Move{From: p.From + d[len(p.To):], To: d})
			}
		default:
			out = append(out, p)
		}
	}
	return out
}

// Decision is the per-move runtime action derived from the two live states.
type Decision int

const (
	DecisionMove Decision = iota // source has it, dest doesn't → move
	DecisionSkip                 // dest already has it → idempotent skip
)

// decide is the fail-closed idempotency table for one (from in source, to in
// dest) pair, given the address sets of both live states.
func decide(srcAddrs, dstAddrs AddressSet, from, to string) (Decision, error) {
	_, inSrc := srcAddrs[from]
	_, inDst := dstAddrs[to]
	switch {
	case inSrc && !inDst:
		return DecisionMove, nil
	case !inSrc && inDst:
		return DecisionSkip, nil
	case inSrc && inDst:
		return 0, fmt.Errorf("ambiguous: %q is in the source state AND %q is in the destination state (would duplicate)", from, to)
	default:
		return 0, fmt.Errorf("missing: %q is not in the source state and %q is not in the destination state (manifest wrong or already pruned)", from, to)
	}
}

// stateAddresses collects every resource address in a state (root + child modules).
func stateAddresses(s *tfjson.State) AddressSet {
	out := AddressSet{}
	if s == nil || s.Values == nil {
		return out
	}
	var walk func(m *tfjson.StateModule)
	walk = func(m *tfjson.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			if r.Mode != tfjson.DataResourceMode {
				out[r.Address] = r.ProviderName
			}
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(s.Values.RootModule)
	return out
}

var implicitProviders = map[string]bool{
	"random":   true,
	"null":     true,
	"local":    true,
	"tls":      true,
	"archive":  true,
	"template": true,
	"time":     true,
	// The built-in provider terraform.io/builtin/terraform backs terraform_data
	// (and terraform_remote_state). It is always present and cannot be declared
	// in required_providers or a provider block, so the destination-provider
	// check can never find it — treat it as always-satisfied.
	"terraform": true,
}

// modulePrefix strips the trailing resource-type.resource-name components from a
// Terraform address, returning just the module path. For a root-module resource
// like "aws_s3_bucket.x" it returns "". For "module.a.aws_s3_bucket.x" it
// returns "module.a". For nested "module.a.module.b.res.x" it returns
// "module.a.module.b".
func modulePrefix(addr string) string {
	parts := strings.Split(addr, ".")
	// Walk backwards: skip the last two non-module components (type + name).
	// Module components start with "module" and appear in pairs (module, name).
	end := len(parts)
	// The resource type and name are the last two segments that don't form a
	// module call. Strip them.
	if end >= 2 && parts[end-2] != "module" {
		end -= 2
	}
	return strings.Join(parts[:end], ".")
}

// DataSourceOrphans returns the data-source addresses from dataSources that
// fall under any declared pair's From prefix. These remain in the source stack
// after the move because stateAddresses filters data sources out of the move
// set — the operator may need to forget them (terraform state rm) before the
// source stack can be retired.
func DataSourceOrphans(pairs []Move, dataSources []string) []string {
	var orphans []string
	for _, ds := range dataSources {
		for _, p := range pairs {
			if matches(ds, p.From) {
				orphans = append(orphans, ds)
				break
			}
		}
	}
	return orphans
}

// IsSpent reports whether all declared moves have already been applied: every
// pair's To-prefix has at least one address present in dstPriorAddrs. Returns
// false when dstPriorAddrs is empty (indeterminate) or pairs is empty.
func IsSpent(pairs []Move, dstPriorAddrs AddressSet) bool {
	if len(dstPriorAddrs) == 0 || len(pairs) == 0 {
		return false
	}
	for _, p := range pairs {
		found := false
		for a := range dstPriorAddrs {
			if matches(a, p.To) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ValidateMovePlan validates a cross-state move manifest against source and destination AddressSets and configured providers.
func ValidateMovePlan(src, dst AddressSet, providers DestProviders, m XMove, isApply bool) []Diagnostic {
	var diags []Diagnostic

	getShortName := func(fullName string) string {
		parts := strings.Split(fullName, "/")
		return parts[len(parts)-1]
	}

	// Expand the pairs against src and dst
	expanded := expandPairs(src, dst, m.Pairs)

	for _, p := range expanded {
		prov, inSrc := src[p.From]
		dstProv, inDst := dst[p.To]

		if isApply {
			// Apply-time validation against live states
			switch {
			case inSrc && !inDst:
				// Valid move to be executed.
				// Verify destination provider configuration
				if prov != "" {
					shortProv := getShortName(prov)
					if !implicitProviders[shortProv] && shortProv != "module" && !providers[shortProv] {
						diags = append(diags, Diagnostic{
							Code:     "xmove/provider-missing",
							Severity: SeverityError,
							Message:  fmt.Sprintf("destination stack has no %q provider configured, but will receive resource %q requiring it", shortProv, p.To),
						})
					}
				}

			case !inSrc && inDst:
				// Valid skip (already moved)

			case inSrc && inDst:
				// Duplicate/occupied error
				diags = append(diags, Diagnostic{
					Code:     "xmove/dest-occupied",
					Severity: SeverityError,
					Message:  fmt.Sprintf("ambiguous: %q is in the source state AND %q is in the destination state (would duplicate)", p.From, p.To),
				})

			default:
				// Missing error (neither has it)
				diags = append(diags, Diagnostic{
					Code:     "xmove/source-missing",
					Severity: SeverityError,
					Message:  fmt.Sprintf("missing: %q is not in the source state and %q is not in the destination state (manifest wrong or already pruned)", p.From, p.To),
				})
			}
		} else {
			// Plan-time validation against planned changes
			// 1. Source address check: must be present in the source plan as a change
			if !inSrc {
				diags = append(diags, Diagnostic{
					Code:     "xmove/source-missing",
					Severity: SeverityError,
					Message:  fmt.Sprintf("address %q not found in source plan (manifest stale or address renamed)", p.From),
				})
			} else {
				// Verify destination provider configuration
				if prov != "" {
					shortProv := getShortName(prov)
					if !implicitProviders[shortProv] && shortProv != "module" && !providers[shortProv] {
						diags = append(diags, Diagnostic{
							Code:     "xmove/provider-missing",
							Severity: SeverityError,
							Message:  fmt.Sprintf("destination stack has no %q provider configured, but will receive resource %q requiring it", shortProv, p.To),
						})
					}
				}
			}

			// 2. Destination check (if destination plan changes are available)
			if len(dst) > 0 && !inDst {
				diags = append(diags, Diagnostic{
					Code:     "xmove/dest-missing",
					Severity: SeverityError,
					Message:  fmt.Sprintf("address %q not found in destination plan changes (manifest stale or target address incorrect)", p.To),
				})
			}

			// Provider mismatch: both addresses present but different providers.
			// When the exact dest address is present, compare directly. When the
			// dest address isn't an exact match (e.g. different resource type across
			// a module-level pair), find the actual provider for any address in dst
			// that shares the same module prefix as p.To.
			effectiveDstProv := dstProv
			if !inDst && len(dst) > 0 {
				toMod := modulePrefix(p.To)
				for addr, ap := range dst {
					if toMod == "" {
						// root module: any address is a candidate
						if !strings.Contains(addr, ".module.") && !strings.HasPrefix(addr, "module.") {
							effectiveDstProv = ap
							break
						}
					} else if matches(addr, toMod) {
						effectiveDstProv = ap
						break
					}
				}
			}
			if inSrc && len(dst) > 0 && prov != "" && effectiveDstProv != "" && prov != effectiveDstProv {
				diags = append(diags, Diagnostic{
					Code:     "xmove/provider-mismatch",
					Severity: SeverityError,
					Message:  fmt.Sprintf("provider mismatch: %q uses %s but destination %q uses %s", p.From, getShortName(prov), p.To, getShortName(effectiveDstProv)),
				})
			}
		}
	}

	return diags
}

// DiscoverDestProviders scans a stack directory for defined providers in HCL config.
func DiscoverDestProviders(stackDir string) DestProviders {
	providers := DestProviders{}
	entries, err := os.ReadDir(stackDir)
	if err != nil {
		return providers
	}

	re := regexp.MustCompile(`provider\s+"([^"]+)"\s*\{`)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tf") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(stackDir, entry.Name()))
		if err != nil {
			continue
		}

		matches := re.FindAllSubmatch(content, -1)
		for _, m := range matches {
			if len(m) > 1 {
				providers[string(m[1])] = true
			}
		}
	}
	return providers
}
