package statemove

import (
	"slices"
	"testing"
)

func TestPriorStateDataSources(t *testing.T) {
	planJSON := []byte(`{"prior_state":{"values":{"root_module":{"resources":[
		{"address":"google_project.p","mode":"managed","provider_name":"registry.terraform.io/hashicorp/google","values":{}},
		{"address":"data.google_secret_manager_secret_version.x","mode":"data","provider_name":"registry.terraform.io/hashicorp/google","values":{}}
	],"child_modules":[{"resources":[
		{"address":"module.m.aws_s3_bucket.b","mode":"managed","provider_name":"registry.terraform.io/hashicorp/aws","values":{}},
		{"address":"module.m.data.aws_caller_identity.current","mode":"data","provider_name":"registry.terraform.io/hashicorp/aws","values":{}}
	]}]}}}}`)

	ds := PriorStateDataSources(planJSON)
	if !slices.Contains(ds, "data.google_secret_manager_secret_version.x") {
		t.Error("root data source must be returned")
	}
	if !slices.Contains(ds, "module.m.data.aws_caller_identity.current") {
		t.Error("child module data source must be returned")
	}
	if slices.Contains(ds, "google_project.p") {
		t.Error("managed resource must not be returned")
	}
	if slices.Contains(ds, "module.m.aws_s3_bucket.b") {
		t.Error("managed child resource must not be returned")
	}

	// No prior_state → nil.
	if got := PriorStateDataSources([]byte(`{"format_version":"1.2"}`)); got != nil {
		t.Errorf("no prior_state must return nil, got %v", got)
	}
}

// D4: DataSourceOrphans returns data-source addresses that fall under any pair's
// From prefix — these remain in the source stack after the move because
// stateAddresses filters data sources out of the move set.
func TestDataSourceOrphans(t *testing.T) {
	pairs := []Move{{From: "module.x", To: "module.y"}}
	dataSources := []string{
		"module.x.data.google_secret_manager_secret_version.v",
		"module.z.data.something.else",
		"data.other.ds",
	}
	orphans := DataSourceOrphans(pairs, dataSources)
	if len(orphans) != 1 || orphans[0] != "module.x.data.google_secret_manager_secret_version.v" {
		t.Errorf("expected one orphan under module.x, got %v", orphans)
	}

	// Data source NOT under any From prefix → no orphans.
	if got := DataSourceOrphans(pairs, []string{"data.other.ds"}); len(got) != 0 {
		t.Errorf("expected no orphans for data source outside from prefix, got %v", got)
	}

	// Multiple pairs: each checked independently.
	multi := []Move{{From: "module.a", To: "module.b"}, {From: "module.c", To: "module.d"}}
	ds2 := []string{
		"module.a.data.ds.x",
		"module.c.data.ds.y",
		"module.z.data.ds.z",
	}
	got := DataSourceOrphans(multi, ds2)
	if len(got) != 2 {
		t.Errorf("expected 2 orphans for two matching prefixes, got %v", got)
	}
}
