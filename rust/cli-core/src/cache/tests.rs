use super::*;

const ONE: &str = r#"
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 5.0.0, < 7.0.0"
  hashes = [
    "h1:dummy",
  ]
}
"#;

// Go: TestParseLockFile.
#[test]
fn parse_lock_file_one() {
    assert_eq!(
        parse_lock_file(ONE).unwrap(),
        vec![Provider {
            address: "registry.terraform.io/hashicorp/google".into(),
            version: "6.10.0".into()
        }]
    );
}

// Go: TestParseLockFile_Multiple.
#[test]
fn parse_lock_file_multiple() {
    let src = r#"
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 5.0.0, < 7.0.0"
  hashes = [
    "h1:dummy1",
  ]
}

provider "registry.terraform.io/hashicorp/aws" {
  version     = "5.32.0"
  hashes = [
    "h1:dummy2",
  ]
}
"#;
    let ps = parse_lock_file(src).unwrap();
    assert_eq!(ps.len(), 2);
    assert_eq!(
        (ps[1].address.as_str(), ps[1].version.as_str()),
        ("registry.terraform.io/hashicorp/aws", "5.32.0")
    );
}

// Go: TestParseLockFile_InvalidHCL.
#[test]
fn parse_lock_file_invalid_hcl() {
    let err = parse_lock_file("provider \"x\" {\n  version = \n").unwrap_err();
    assert!(err.starts_with("hcl parse: "), "{err}");
}

#[test]
fn parse_lock_file_decode_errors() {
    let err = parse_lock_file("provider \"x\" {\n}\n").unwrap_err();
    assert!(
        err.starts_with("hcl decode: Missing required argument"),
        "{err}"
    );
    let err = parse_lock_file("provider \"x\" {\n version = \"1\"\n nope = 1\n}\n").unwrap_err();
    assert!(err.starts_with("hcl decode: Unsupported argument"), "{err}");
}

#[test]
fn keys_and_layout() {
    assert_eq!(
        object_key(
            "v1",
            "registry.terraform.io/hashicorp/null",
            "3.2.1",
            "linux_amd64"
        ),
        "v1/registry.terraform.io/hashicorp/null/3.2.1/linux_amd64.tar.gz"
    );
    assert_eq!(
        object_key("0", "a/b/c", "1", "p"),
        object_key("", "a/b/c", "1", "p")
    );
    assert_eq!(object_key("", "a/b/c", "1", "p"), "a/b/c/1/p.tar.gz");
    assert_eq!(
        binary_name("registry.terraform.io/hashicorp/null"),
        "terraform-provider-null"
    );
    assert_eq!(
        address_version_of("registry.terraform.io/hashicorp/aws/5.0.0/linux_amd64"),
        Some(("registry.terraform.io/hashicorp/aws".into(), "5.0.0".into()))
    );
    assert_eq!(address_version_of("x/1.0/linux_amd64"), None);
    if cfg!(all(target_os = "linux", target_arch = "x86_64")) {
        assert_eq!(platform(), "linux_amd64");
    }
}

#[test]
fn download_urls_route_by_host() {
    let p = |a: &str| Provider {
        address: a.into(),
        version: "6.10.0".into(),
    };
    let url = |a: &str| {
        download_api_url(
            "registry.terraform.io",
            "http://fake",
            &p(a),
            "linux",
            "amd64",
        )
    };
    assert_eq!(
        url("hashicorp/google").unwrap(),
        "http://fake/v1/providers/hashicorp/google/6.10.0/download/linux/amd64"
    );
    assert_eq!(
        url("registry.terraform.io/hashicorp/google").unwrap(),
        "http://fake/v1/providers/hashicorp/google/6.10.0/download/linux/amd64"
    );
    assert_eq!(
        url("registry.example.com/acme/thing").unwrap(),
        "https://registry.example.com/v1/providers/acme/thing/6.10.0/download/linux/amd64"
    );
    assert_eq!(
        url("lonely").unwrap_err(),
        "invalid provider address: lonely"
    );
    assert_eq!(
        decode_download_url(br#"{"download_url": "http://example.com/binary.zip"}"#).unwrap(),
        "http://example.com/binary.zip"
    );
    assert!(decode_download_url(b"nope").is_err());
}
