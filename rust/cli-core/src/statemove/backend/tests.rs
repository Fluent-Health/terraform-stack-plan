use super::*;

// Go: TestGCSBackend (cmd/tfstackplan/gcslock_test.go), over file contents.
#[test]
fn gcs_backend_reads_bucket_and_prefix() {
    let cfg = "terraform {\n  backend \"gcs\" {\n    bucket = \"my-tf-state\"\n    prefix = \"stacks/foo\"\n  }\n}\n";
    assert_eq!(
        gcs_backend([cfg]),
        Some(("my-tf-state".to_string(), "stacks/foo".to_string()))
    );
    assert_eq!(gcs_backend(["resource \"null_resource\" \"x\" {}\n"]), None);
    // Unparseable files are skipped; the first file with a bucket wins.
    assert_eq!(
        gcs_backend(["terraform {", cfg]),
        Some(("my-tf-state".to_string(), "stacks/foo".to_string()))
    );
    assert_eq!(
        gcs_backend(["terraform {\n  backend \"local\" {}\n}\n"]),
        None
    );
}
