package plan

import (
	"os"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func load(t *testing.T, name string) RawStack {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Parse("stack", data)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestParseCountsAndAttrs(t *testing.T) {
	rs := load(t, "update.json")
	if rs.Counts != (model.Counts{Change: 1}) {
		t.Fatalf("counts = %+v, want Change:1", rs.Counts)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(rs.Changes))
	}
	c := rs.Changes[0]
	if c.Action != model.ActionChange || c.Type != "google_project_iam_member" {
		t.Fatalf("bad change: %+v", c)
	}
	if len(c.Attrs) != 1 || c.Attrs[0].Name != "role" {
		t.Fatalf("expected one changed attr 'role', got %+v", c.Attrs)
	}
}

func TestParseMixedActions(t *testing.T) {
	rs := load(t, "mixed.json")
	want := model.Counts{Add: 1, Destroy: 1, Replace: 1}
	if rs.Counts != want {
		t.Fatalf("counts = %+v, want %+v", rs.Counts, want)
	}
	if len(rs.Changes) != 3 {
		t.Fatalf("got %d changes (no-op should be excluded), want 3", len(rs.Changes))
	}
	for _, c := range rs.Changes {
		switch c.Action {
		case model.ActionAdd:
			// create should expose after-attrs
			if len(c.Attrs) == 0 {
				t.Fatalf("create should have attrs, got none for %s", c.Address)
			}
			if c.Attrs[0].Name != "name" || c.Attrs[0].After != "b" {
				t.Fatalf("create attr mismatch: %+v", c.Attrs)
			}
		case model.ActionDestroy:
			// delete should expose before-attrs
			if len(c.Attrs) == 0 {
				t.Fatalf("delete should have attrs, got none for %s", c.Address)
			}
			if c.Attrs[0].Name != "name" || c.Attrs[0].Before != "b" {
				t.Fatalf("delete attr mismatch: %+v", c.Attrs)
			}
		}
	}
}

func TestForgetActionSurfaced(t *testing.T) {
	rs := load(t, "forget.json")
	// forget is now surfaced: the update plus the forget, with Forget counted.
	if rs.Counts != (model.Counts{Change: 1, Forget: 1}) {
		t.Fatalf("counts = %+v, want Change:1 Forget:1", rs.Counts)
	}
	if len(rs.Changes) != 2 {
		t.Fatalf("want 2 changes (update + forget), got %d", len(rs.Changes))
	}
	var fg *RawChange
	for i := range rs.Changes {
		if rs.Changes[i].Address == "a.forget" {
			fg = &rs.Changes[i]
		}
	}
	if fg == nil {
		t.Fatal("forget change missing")
	}
	if fg.Action != model.ActionForget {
		t.Fatalf("forget action = %q, want forget", fg.Action)
	}
	if len(fg.Attrs) == 0 || fg.Attrs[0].Before != "b" {
		t.Fatalf("forget should carry before-attrs, got %+v", fg.Attrs)
	}
}

func TestMovedAndImported(t *testing.T) {
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"a.new","previous_address":"a.old","type":"t","name":"new",
	     "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"b.imported","type":"t","name":"imported",
	     "change":{"actions":["no-op"],"importing":{"id":"i-0abc"},
	       "before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"c.skip","type":"t","name":"skip",
	     "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Counts.Move != 1 || rs.Counts.Import != 1 {
		t.Fatalf("counts = %+v, want Move:1 Import:1", rs.Counts)
	}
	if len(rs.Changes) != 2 { // plain no-op c.skip is dropped
		t.Fatalf("want 2 changes (moved + imported), got %d", len(rs.Changes))
	}
	byAddr := map[string]RawChange{}
	for _, c := range rs.Changes {
		byAddr[c.Address] = c
	}
	if mv := byAddr["a.new"]; !mv.Moved || mv.PreviousAddress != "a.old" || mv.Action != model.ActionNoop {
		t.Fatalf("moved parse wrong: %+v", mv)
	}
	if im := byAddr["b.imported"]; !im.Imported || im.ImportID != "i-0abc" || im.Action != model.ActionNoop {
		t.Fatalf("imported parse wrong: %+v", im)
	}
}

func TestSensitiveAndUnknownAndPartialChange(t *testing.T) {
	rs := load(t, "sensitive.json")
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(rs.Changes))
	}
	attrs := rs.Changes[0].Attrs
	byName := map[string]RawAttr{}
	for _, a := range attrs {
		byName[a.Name] = a
	}
	// "unchanged" is identical before/after and not unknown → must be excluded
	if _, ok := byName["unchanged"]; ok {
		t.Fatal("unchanged attribute should not be included")
	}
	// "secret_data" changed and is sensitive
	sd, ok := byName["secret_data"]
	if !ok || !sd.Sensitive {
		t.Fatalf("secret_data should be present and Sensitive, got %+v", sd)
	}
	// "computed_id" is unchanged value but marked unknown → must be included with Unknown=true
	ci, ok := byName["computed_id"]
	if !ok || !ci.Unknown {
		t.Fatalf("computed_id should be present and Unknown, got %+v", ci)
	}
}

func TestNestedSensitiveMarkerDoesNotFlagWholeAttr(t *testing.T) {
	// kubernetes_deployment_v1-style: only a deep leaf (password) is sensitive,
	// so after_sensitive.spec is a NESTED object, not a bare `true`. The whole
	// `spec` attribute must NOT be flagged sensitive — instead the subtree is
	// carried so the differ can redact only the leaf.
	data := []byte(`{
	  "format_version":"1.2",
	  "resource_changes":[
	    {"address":"k.app","type":"kubernetes_deployment_v1","name":"app",
	     "change":{"actions":["update"],
	       "before":{"spec":{"cpu":"334m","password":"x"}},
	       "after": {"spec":{"cpu":"300m","password":"x"}},
	       "after_unknown":{},
	       "before_sensitive":{"spec":{"password":true}},
	       "after_sensitive": {"spec":{"password":true}}}}
	  ]}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	var spec *RawAttr
	for i := range rs.Changes[0].Attrs {
		if rs.Changes[0].Attrs[i].Name == "spec" {
			spec = &rs.Changes[0].Attrs[i]
		}
	}
	if spec == nil {
		t.Fatal("spec attr missing")
	}
	if spec.Sensitive {
		t.Fatalf("nested sensitivity must NOT flag the whole attr sensitive: %+v", spec)
	}
	sub, ok := spec.AfterSensitive.(map[string]any)
	if !ok || sub["password"] != true {
		t.Fatalf("spec must carry the nested sensitivity subtree, got %+v", spec.AfterSensitive)
	}
}

func TestParseCreateExtractsAfterAttrs(t *testing.T) {
	rs := load(t, "create.json")
	if rs.Counts.Add != 1 {
		t.Fatalf("Add count = %d, want 1", rs.Counts.Add)
	}
	got := map[string]RawAttr{}
	for _, a := range rs.Changes[0].Attrs {
		got[a.Name] = a
	}
	if got["account_id"].After != "app-api" {
		t.Errorf("account_id After = %v, want app-api", got["account_id"].After)
	}
	if !got["unique_id"].Unknown {
		t.Errorf("unique_id should be known-after-apply")
	}
}

func TestParseCarriesModuleAndName(t *testing.T) {
	data := []byte(`{"format_version":"1.2","resource_changes":[
	  {"address":"module.net.google_compute_firewall.web","module_address":"module.net",
	   "type":"google_compute_firewall","name":"web",
	   "change":{"actions":["update"],"before":{"a":"1"},"after":{"a":"2"},
	     "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	c := rs.Changes[0]
	if c.Name != "web" || c.ModuleAddress != "module.net" {
		t.Fatalf("got name=%q module=%q", c.Name, c.ModuleAddress)
	}
}

func TestParseRawRetainsUnchangedScalarsSkipsSensitiveAndNested(t *testing.T) {
	// In-place update: only "role" changes; "project" is unchanged; "secret"
	// is sensitive; "labels" is a nested object. Raw must keep role+project
	// (scalars), drop secret (sensitive) and labels (non-scalar).
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
	     "change":{"actions":["update"],
	       "before":{"role":"roles/viewer","project":"p1","secret":"old","labels":{"a":"b"}},
	       "after":{"role":"roles/editor","project":"p1","secret":"new","labels":{"a":"b"}},
	       "after_unknown":{},
	       "before_sensitive":{"secret":true},"after_sensitive":{"secret":true}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(rs.Changes))
	}
	raw := rs.Changes[0].Raw
	if raw["project"] != "p1" {
		t.Errorf("Raw[project] = %v, want p1 (must survive even though unchanged)", raw["project"])
	}
	if raw["role"] != "roles/editor" {
		t.Errorf("Raw[role] = %v, want roles/editor (after wins)", raw["role"])
	}
	if _, ok := raw["secret"]; ok {
		t.Error("Raw must not include sensitive attribute 'secret'")
	}
	if _, ok := raw["labels"]; ok {
		t.Error("Raw must not include non-scalar attribute 'labels'")
	}
}

func TestParseRawPrefersAfterFallsBackToBefore(t *testing.T) {
	// Delete: after is null, so Raw comes from before.
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
	     "change":{"actions":["delete"],
	       "before":{"project":"p9"},"after":null,
	       "before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Changes[0].Raw["project"] != "p9" {
		t.Errorf("Raw[project] = %v, want p9 (from before on delete)", rs.Changes[0].Raw["project"])
	}
}

func TestParseRawSkipsSensitiveEitherSideAndComputed(t *testing.T) {
	// "mode" is sensitive on the after side only (value lives non-sensitive in
	// before); "id" is known-after-apply. Both must be excluded; "role" kept.
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"x.y","type":"t","name":"y",
	     "change":{"actions":["update"],
	       "before":{"role":"roles/a","mode":"old","id":"old-id"},
	       "after":{"role":"roles/b","mode":null,"id":null},
	       "after_unknown":{"id":true},
	       "before_sensitive":{},"after_sensitive":{"mode":true}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	raw := rs.Changes[0].Raw
	if raw["role"] != "roles/b" {
		t.Errorf("Raw[role] = %v, want roles/b", raw["role"])
	}
	if _, ok := raw["mode"]; ok {
		t.Error("Raw must skip attr sensitive on the after side even if before value is non-sensitive")
	}
	if _, ok := raw["id"]; ok {
		t.Error("Raw must skip known-after-apply (computed) attr")
	}
}

func TestParseRawDerivesProjectFromGCPParentWhenComputed(t *testing.T) {
	// Net-new google_secret_manager_secret_iam_member: `project` is computed
	// (known-after-apply) so it is absent from `after`, but the project is still
	// knowable from the `secret_id` parent path "projects/<P>/secrets/...".
	// Consumers keying on Raw["project"] (e.g. a per-project IAM gate) must see
	// the project, else a brand-new binding yields no target.
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"module.w.google_secret_manager_secret_iam_member.ci[\"x\"]",
	     "type":"google_secret_manager_secret_iam_member","name":"ci",
	     "change":{"actions":["create"],"before":null,
	       "after":{"secret_id":"projects/fh-prod-svc/secrets/website_instagram_access_token","role":"roles/secretmanager.secretAccessor","member":"serviceAccount:ci-website@fh-prod-svc.iam.gserviceaccount.com"},
	       "after_unknown":{"id":true,"project":true,"etag":true},
	       "before_sensitive":false,"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Changes[0].Raw["project"]; got != "fh-prod-svc" {
		t.Errorf("Raw[project] = %v, want fh-prod-svc (derived from secret_id parent path)", got)
	}
}

func TestParseRawDoesNotOverrideKnownProjectOrFabricate(t *testing.T) {
	// A known `project` must win over any derivable parent path, and a resource
	// with no "projects/<id>/..." scalar must not gain a fabricated project.
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"a","type":"google_project_iam_member","name":"a",
	     "change":{"actions":["create"],"before":null,
	       "after":{"project":"real-proj","secret_id":"projects/other-proj/secrets/x","role":"roles/viewer"},
	       "after_unknown":{},"before_sensitive":false,"after_sensitive":{}}},
	    {"address":"b","type":"google_storage_bucket_iam_member","name":"b",
	     "change":{"actions":["create"],"before":null,
	       "after":{"bucket":"my-bucket","role":"roles/viewer"},
	       "after_unknown":{"project":true},"before_sensitive":false,"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Changes[0].Raw["project"]; got != "real-proj" {
		t.Errorf("Raw[project] = %v, want real-proj (explicit value must not be overridden)", got)
	}
	if _, ok := rs.Changes[1].Raw["project"]; ok {
		t.Error("must not fabricate a project when no projects/<id>/ parent path exists")
	}
}

func TestParseProviderName(t *testing.T) {
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {
	      "address": "a.b",
	      "type": "t",
	      "name": "b",
	      "provider_name": "registry.terraform.io/hashicorp/google",
	      "change": {
	        "actions": ["create"],
	        "before": null,
	        "after": {"x": "1"},
	        "after_unknown": {},
	        "before_sensitive": {},
	        "after_sensitive": {}
	      }
	    }
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(rs.Changes))
	}
	c := rs.Changes[0]
	if c.ProviderName != "registry.terraform.io/hashicorp/google" {
		t.Fatalf("provider_name = %q, want registry.terraform.io/hashicorp/google", c.ProviderName)
	}
}

func TestParseSkipsDataSources(t *testing.T) {
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
		{
		  "address": "data.aws_caller_identity.current",
		  "mode": "data",
		  "type": "aws_caller_identity",
		  "name": "current",
		  "provider_name": "registry.terraform.io/hashicorp/aws",
		  "change": {
			"actions": ["noop"]
		  }
		},
		{
		  "address": "aws_s3_bucket.main",
		  "mode": "managed",
		  "type": "aws_s3_bucket",
		  "name": "main",
		  "provider_name": "registry.terraform.io/hashicorp/aws",
		  "change": {
			"actions": ["create"]
		  }
		}
	  ]
	}`)
	rs, err := Parse("stack", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("expected 1 change (data source skipped), got %d: %+v", len(rs.Changes), rs.Changes)
	}
	if rs.Changes[0].Address != "aws_s3_bucket.main" {
		t.Errorf("expected aws_s3_bucket.main, got %s", rs.Changes[0].Address)
	}
}

func TestParseSensitivityOnlyChange(t *testing.T) {
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
		{
		  "address": "aws_ssm_parameter.foo",
		  "mode": "managed",
		  "type": "aws_ssm_parameter",
		  "name": "foo",
		  "provider_name": "registry.terraform.io/hashicorp/aws",
		  "change": {
			"actions": ["update"],
			"before": { "value": "my-secret-val" },
			"after": { "value": "my-secret-val" },
			"before_sensitive": { "value": false },
			"after_sensitive": { "value": true }
		  }
		}
	  ]
	}`)
	rs, err := Parse("stack", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(rs.Changes))
	}
	ch := rs.Changes[0]
	if !ch.SensitivityOnly {
		t.Error("resource change should be marked SensitivityOnly")
	}
	if len(ch.Attrs) != 1 {
		t.Fatalf("expected 1 changed attribute, got %d", len(ch.Attrs))
	}
	attr := ch.Attrs[0]
	if attr.Name != "value" || !attr.SensitivityOnly {
		t.Errorf("expected value attribute to be SensitivityOnly, got %+v", attr)
	}
	if rs.Counts.SensitivityOnly != 1 || rs.Counts.Change != 1 {
		t.Errorf("expected counts 1 Change & 1 SensitivityOnly, got %+v", rs.Counts)
	}
}

