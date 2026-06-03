package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden regenerates the committed examples/*.md goldens:
//
//	go test ./cmd/tfstackplan -update
var updateGolden = flag.Bool("update", false, "regenerate golden example files under examples/")

// exampleCfgHCL is the classification policy used for every example: a `safe`
// default, the `iam` preset, and a `destructive` rule.
const exampleCfgHCL = `classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon = "🔐"
  }
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
`

// exampleStacks writes the shared multi-stack input (56 changes across 8 stacks,
// including IAM, sensitive, destructive, structural and large-diff resources)
// into an out/<name>/tfplan.json tree and returns the plans dir.
func exampleStacks(t *testing.T, dir string) string {
	t.Helper()
	stacks := []struct {
		name    string
		changes []change
	}{
		{"platform/nonprod", []change{
			iamUpdate("google_project_iam_member.data_engineers"),
			iamUpdate("google_project_iam_member.viewers"),
			structuralUpdate("google_storage_bucket.tfstate", 0),
			structuralUpdate("google_storage_bucket.assets", 1),
		}},
		{"service-projects/app-dev", []change{
			create("google_service_account.api", "google_service_account"),
			create("google_pubsub_topic.events", "google_pubsub_topic"),
			create("google_cloud_run_service.api", "google_cloud_run_service"),
			create("google_cloud_run_service.worker", "google_cloud_run_service"),
			structuralUpdate("google_storage_bucket.uploads", 2),
			structuralUpdate("google_storage_bucket.exports", 3),
			sensitiveUpdate("google_secret_manager_secret_version.db_password"),
		}},
		{"service-projects/app-test", []change{
			structuralUpdate("google_storage_bucket.b0", 4),
			structuralUpdate("google_storage_bucket.b1", 5),
			structuralUpdate("google_storage_bucket.b2", 6),
			structuralUpdate("google_storage_bucket.b3", 7),
			structuralUpdate("google_storage_bucket.b4", 8),
			structuralUpdate("google_storage_bucket.b5", 9),
			structuralUpdate("google_storage_bucket.b6", 10),
			structuralUpdate("google_storage_bucket.b7", 11),
		}},
		{"service-projects/app-prod", []change{
			iamUpdate("google_project_iam_member.deployers"),
			bigUpdate("kubernetes_config_map.app_config", 90),
			structuralUpdate("google_storage_bucket.prod_state", 12),
			structuralUpdate("google_storage_bucket.prod_assets", 13),
			structuralUpdate("google_storage_bucket.prod_logs", 14),
			structuralUpdate("google_storage_bucket.prod_backups", 15),
		}},
		{"data/warehouse", []change{
			del("google_project_iam_member.legacy_admins", "google_project_iam_member"),
			del("google_bigquery_dataset.legacy_users", "google_bigquery_dataset"),
			del("google_storage_bucket.legacy_exports", "google_storage_bucket"),
			del("google_storage_bucket.legacy_imports", "google_storage_bucket"),
			del("google_pubsub_topic.legacy_stream", "google_pubsub_topic"),
			del("google_pubsub_subscription.legacy_sub", "google_pubsub_subscription"),
		}},
		{"networking/shared-vpc", []change{
			structuralUpdate("google_compute_subnetwork.s0", 16),
			structuralUpdate("google_compute_subnetwork.s1", 17),
			structuralUpdate("google_compute_subnetwork.s2", 18),
			structuralUpdate("google_compute_firewall.allow_internal", 19),
			structuralUpdate("google_compute_firewall.allow_health", 20),
			replace("google_compute_instance.bastion", "google_compute_instance",
				map[string]any{"machine_type": "e2-small"},
				map[string]any{"machine_type": "e2-medium"}),
			replace("google_compute_address.nat", "google_compute_address",
				map[string]any{"address_type": "INTERNAL"},
				map[string]any{"address_type": "EXTERNAL"}),
		}},
		{"observability/grafana", []change{
			create("helm_release.grafana", "helm_release"),
			create("helm_release.loki", "helm_release"),
			create("kubernetes_namespace.observability", "kubernetes_namespace"),
			create("kubernetes_service_account.grafana", "kubernetes_service_account"),
			create("kubernetes_secret.grafana_admin", "kubernetes_secret"),
			bigUpdate("kubernetes_config_map.dashboards", 120),
			yamlUpdate("kubernetes_manifest.ingress", 2),
			yamlUpdate("kubernetes_manifest.configmap", 11),
			structuralUpdate("google_storage_bucket.grafana_state", 21),
			structuralUpdate("google_storage_bucket.loki_chunks", 22),
			structuralUpdate("google_storage_bucket.loki_ruler", 23),
		}},
		{"security/secrets", []change{
			sensitiveUpdate("google_secret_manager_secret_version.api_key"),
			sensitiveUpdate("google_secret_manager_secret_version.tls_cert"),
			sensitiveUpdate("google_secret_manager_secret_version.oauth_secret"),
			sensitiveUpdate("google_secret_manager_secret_version.signing_key"),
			iamUpdate("google_project_iam_member.secret_accessors"),
			iamUpdate("google_project_iam_member.secret_admins"),
			structuralUpdate("google_storage_bucket.audit_logs", 24),
			structuralUpdate("google_storage_bucket.backups", 25),
			structuralUpdate("google_storage_bucket.archive", 26),
		}},
	}

	plansDir := filepath.Join(dir, "out")
	for _, s := range stacks {
		p := filepath.Join(plansDir, filepath.FromSlash(s.name), "tfplan.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, genPlan(s.changes...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return plansDir
}

// stateOpsStacks writes the input for the state-ops / structured-diff example:
// moved / imported / forget / nested-block resources, plus rich nested JSON and
// YAML diffs in small (inline) and big (folded) variants.
func stateOpsStacks(t *testing.T, dir string) string {
	t.Helper()
	stacks := []struct {
		name    string
		changes []change
	}{
		{"infra/migrations", []change{
			moved("google_storage_bucket.assets", "google_storage_bucket.legacy_assets", "google_storage_bucket"),
			movedUpdate("google_storage_bucket.state", "module.old.google_storage_bucket.state"),
			// A moved IAM binding: a pure state op, so it must NOT badge the stack iam.
			moved("google_project_iam_member.viewers", "google_project_iam_member.legacy_viewers", "google_project_iam_member"),
			imported("google_project.host", "google_project", "my-host-project"),
			forget("aws_s3_bucket.legacy", "aws_s3_bucket"),
			nestedBlockUpdate("google_compute_firewall.web"),
		}},
		{"platform/policies", []change{
			jsonUpdate("aws_iam_policy.small", 1),                    // few paths → inline
			jsonUpdate("aws_iam_policy.big", 12),                     // many paths → folded
			yamlManifestUpdate("kubernetes_manifest.app", false),     // small → inline
			yamlManifestUpdate("kubernetes_manifest.platform", true), // big → folded
		}},
	}
	plansDir := filepath.Join(dir, "out")
	for _, s := range stacks {
		p := filepath.Join(plansDir, filepath.FromSlash(s.name), "tfplan.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, genPlan(s.changes...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return plansDir
}

// longAddressStacks writes the input for the long-names / for-each example:
// deeply nested module paths and for-each ["key"] indices (including a long
// email-ish key and an empty [""] key), a long import id, and a long moved-from
// address — the cases whose summary lines wrap to two lines on GitHub, used to
// judge the row layout (glyph gluing, the import-id sub-line, stack headers).
func longAddressStacks(t *testing.T, dir string) string {
	t.Helper()
	stacks := []struct {
		name    string
		changes []change
	}{
		{"platform/networking-shared-vpc", []change{
			// Deeply nested module path + for-each over subnet regions.
			structuralUpdate(`module.networking.module.shared_vpc.module.subnets.google_compute_subnetwork.private["us-central1-private-primary"]`, 0),
			// for-each over IAM members — the key is a full member principal.
			iamUpdate(`module.platform.module.security.module.iam_bindings.google_project_iam_member.engineers["user:ivan.kerin@fluentinhealth.com"]`),
			// Module refactor rename: a long previous_address.
			moved(
				`module.networking.module.dns.google_dns_managed_zone.internal["internal.fh.example.com"]`,
				`module.dns.google_dns_managed_zone.internal_fh_example_com_legacy`,
				"google_dns_managed_zone"),
			// Import with a long resource-manager id (exercises the <sub> id line).
			imported(
				`module.networking.module.dns.google_dns_record_set.a_records["a.internal.fh.example.com"]`,
				"google_dns_record_set",
				"projects/fh-host-nonprod/managedZones/internal-fh/rrsets/a.internal.fh.example.com./A"),
			// Empty for-each key edge case: name[""].
			structuralUpdate(`module.networking.google_storage_bucket.flow_logs[""]`, 1),
			// Short rows for contrast — these should stay on one line.
			structuralUpdate("google_compute_address.nat", 2),
			iamUpdate("google_project_iam_member.viewers"),
		}},
		{"service-projects/app-prod", []change{
			structuralUpdate(`module.service_projects.module.app_prod.module.workload_identity.google_storage_bucket.runner_state["orchestration-pipeline-runner"]`, 3),
			iamUpdate(`module.service_projects.module.app_prod.module.workload_identity.google_project_iam_member.runner["serviceAccount:orchestration-runner@fh-svc-prod.iam.gserviceaccount.com"]`),
			replace(
				`module.service_projects.module.app_prod.module.compute.google_compute_instance.bastion["primary"]`,
				"google_compute_instance",
				map[string]any{"machine_type": "e2-small"},
				map[string]any{"machine_type": "e2-medium"}),
		}},
	}
	plansDir := filepath.Join(dir, "out")
	for _, s := range stacks {
		p := filepath.Join(plansDir, filepath.FromSlash(s.name), "tfplan.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, genPlan(s.changes...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return plansDir
}

// TestExamples renders the shared input at four byte budgets, exercising the
// full size-budget cascade. The committed examples/*.md files are the goldens:
// each scenario asserts an invariant proving its rendering, then is compared to
// (or, with -update, written to) its example file.
func TestExamples(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "policy.hcl")
	if err := os.WriteFile(cfgPath, []byte(exampleCfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	stacks := exampleStacks(t, dir)

	scenarios := []struct {
		file     string
		maxBytes int
		assert   func(t *testing.T, out string, fits bool)
	}{
		{
			file:     "big-plan.md",
			maxBytes: 60000,
			assert: func(t *testing.T, out string, fits bool) {
				if !fits {
					t.Errorf("big-plan: expected report to fit 60 KB budget")
				}
				if !strings.Contains(out, "<details>") {
					t.Errorf("big-plan: expected per-stack <details>")
				}
				if strings.Contains(out, "(hidden to fit size limit)") {
					t.Errorf("big-plan: expected no per-attribute simplification")
				}
				if strings.Contains(out, "⚠️") {
					t.Errorf("big-plan: expected no cascade notice")
				}
			},
		},
		{
			file:     "over-budget-degraded.md",
			maxBytes: 18000,
			assert: func(t *testing.T, out string, fits bool) {
				if !fits {
					t.Errorf("degraded: expected report to fit after Phase-1 degradation")
				}
				if !strings.Contains(out, "<details>") {
					t.Errorf("degraded: details should be kept in full mode")
				}
				if !strings.Contains(out, "(hidden to fit size limit)") {
					t.Errorf("degraded: expected per-attribute simplification marker")
				}
				if strings.Contains(out, "⚠️") {
					t.Errorf("degraded: should not reach the terminal cascade")
				}
			},
		},
		{
			file:     "over-budget-summary-only.md",
			maxBytes: 2500,
			assert: func(t *testing.T, out string, fits bool) {
				if !fits {
					t.Errorf("summary-only: expected the table to fit the budget")
				}
				if strings.Contains(out, "<details>") {
					t.Errorf("summary-only: all details must be dropped")
				}
				if !strings.Contains(out, "Per-stack detail omitted") {
					t.Errorf("summary-only: expected the summary-only notice")
				}
				if !strings.Contains(out, "| Stack |") {
					t.Errorf("summary-only: the summary table must be kept")
				}
			},
		},
		{
			file:     "over-budget-minimal.md",
			maxBytes: 120,
			assert: func(t *testing.T, out string, fits bool) {
				if fits {
					t.Errorf("minimal: a 120-byte budget cannot fit even the floor")
				}
				if strings.Contains(out, "| Stack |") {
					t.Errorf("minimal: the table must be dropped")
				}
				if !strings.Contains(out, "Per-stack table omitted") {
					t.Errorf("minimal: expected the minimal-floor notice")
				}
				if !strings.Contains(out, "8 stacks") {
					t.Errorf("minimal: expected the one-line aggregate")
				}
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.file, func(t *testing.T) {
			out, fits, err := run(opts{
				plansDir: stacks,
				title:    "Terraform plan — nonprod",
				marker:   "tfstackplan:nonprod",
				config:   cfgPath,
				maxBytes: sc.maxBytes,
				details:  "closed",
			})
			if err != nil {
				t.Fatal(err)
			}
			sc.assert(t, out, fits)
			checkGolden(t, sc.file, out)
		})
	}
}

// checkGolden compares out to examples/<file>, or rewrites it under -update.
func checkGolden(t *testing.T, file, out string) {
	t.Helper()
	golden := filepath.Join("..", "..", "examples", file)
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./cmd/tfstackplan -update`)", file, err)
	}
	if string(want) != out {
		t.Errorf("%s is stale; run `go test ./cmd/tfstackplan -update`.\n%s",
			file, firstDiff(string(want), out))
	}
}

// TestStateOpsExample renders moved / imported / forget / nested-block resources
// and rich nested JSON & YAML diffs (small inline + big folded) into the
// examples/state-ops.md golden.
func TestStateOpsExample(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "policy.hcl")
	if err := os.WriteFile(cfgPath, []byte(exampleCfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	stacks := stateOpsStacks(t, dir)

	out, fits, err := run(opts{
		plansDir: stacks,
		title:    "Terraform plan — state ops & structured diffs",
		marker:   "tfstackplan:state-ops",
		config:   cfgPath,
		maxBytes: 60000,
		details:  "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fits {
		t.Errorf("state-ops: expected report to fit")
	}
	// State operations. The descriptor and the (small, monospaced) import id
	// each drop to their own indented line below the address.
	for _, want := range []string{
		"↪️&nbsp;", "moved from ", "📥&nbsp;", "imported", "<sub>id=<code>", "📤&nbsp;", "forgotten · ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("state-ops: missing %q in output", want)
		}
	}
	// A moved IAM resource is a pure state op (needs no apply-time write
	// permission), so it must NOT flip infra/migrations into the iam guard.
	if !strings.Contains(out, "<b>infra/migrations</b> · ✅ safe") {
		t.Errorf("state-ops: moved IAM resource must not classify infra/migrations as iam:\n%s", out)
	}
	// Move/import/forget surfaced in a Stack cell (table choice B, no columns).
	if !strings.Contains(out, "move") || !strings.Contains(out, "import") || !strings.Contains(out, "forget") {
		t.Errorf("state-ops: expected move/import/forget noted in the table")
	}
	// Structured values render as contextual diffs tagged with their kind.
	if !strings.Contains(out, "(json)") || !strings.Contains(out, "(yaml)") {
		t.Errorf("state-ops: expected (json)/(yaml) kind labels on structured blocks")
	}
	// Context lines + changed -/+ lines, e.g. the firewall port and the policy arn.
	if !strings.Contains(out, `"443"`) {
		t.Errorf("state-ops: expected nested-block contextual diff (port 443):\n%s", out)
	}
	if !strings.Contains(out, "bucket-new-00") {
		t.Errorf("state-ops: expected JSON contextual diff (new arn)")
	}
	if !strings.Contains(out, "app:1.5") {
		t.Errorf("state-ops: expected YAML manifest contextual diff (new image)")
	}
	// A big structured change folds into a closed row.
	if !strings.Contains(out, "<details><summary>〰️&nbsp;aws_iam_policy.big") {
		t.Errorf("state-ops: big JSON should fold to a closed row:\n%s", out)
	}
	checkGolden(t, "state-ops.md", out)
}

// TestLongAddressExample renders deeply nested module paths and for-each
// ["key"] indices into examples/long-names.md, expanded (details=open) so the
// showcase is fully visible when the file is viewed rendered on GitHub. Its
// purpose is to judge how long summary lines wrap to two lines.
func TestLongAddressExample(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "policy.hcl")
	if err := os.WriteFile(cfgPath, []byte(exampleCfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	stacks := longAddressStacks(t, dir)

	out, fits, err := run(opts{
		plansDir: stacks,
		title:    "Terraform plan — long names & for-each",
		marker:   "tfstackplan:long-names",
		config:   cfgPath,
		maxBytes: 60000,
		details:  "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fits {
		t.Errorf("long-names: expected report to fit")
	}
	// A long for-each key survives verbatim into the row summary.
	if !strings.Contains(out, `engineers["user:ivan.kerin@fluentinhealth.com"]`) {
		t.Errorf("long-names: expected the for-each member key in a row summary:\n%s", out)
	}
	// The empty for-each key renders as name[""].
	if !strings.Contains(out, `google_storage_bucket.flow_logs[""]`) {
		t.Errorf("long-names: expected the empty for-each key name[\"\"]:\n%s", out)
	}
	// The long import id lands on its own small, monospaced line.
	if !strings.Contains(out, "<sub>id=<code>projects/fh-host-nonprod/managedZones/internal-fh/rrsets/a.internal.fh.example.com./A</code></sub>") {
		t.Errorf("long-names: expected the import id on its own monospaced sub line:\n%s", out)
	}
	checkGolden(t, "long-names.md", out)
}

// firstDiff returns a short description of where two strings first differ.
func firstDiff(want, got string) string {
	min := len(want)
	if len(got) < min {
		min = len(got)
	}
	for i := 0; i < min; i++ {
		if want[i] != got[i] {
			lo := i - 40
			if lo < 0 {
				lo = 0
			}
			return fmt.Sprintf("first diff at byte %d:\n want: %q\n  got: %q", i, snippet(want, lo, i+40), snippet(got, lo, i+40))
		}
	}
	return fmt.Sprintf("len want=%d got=%d (one is a prefix of the other)", len(want), len(got))
}

func snippet(s string, lo, hi int) string {
	if hi > len(s) {
		hi = len(s)
	}
	if lo > len(s) {
		lo = len(s)
	}
	return s[lo:hi]
}
