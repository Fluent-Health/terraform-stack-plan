use wiremock::matchers::{header, method, path, query_param};
use wiremock::{Mock, MockServer, ResponseTemplate};

use super::*;

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.unwrap()
}

fn read_all(mut b: Body) -> Vec<u8> {
    let mut out = Vec::new();
    b.read_to_end(&mut out).unwrap();
    out
}

// Go: TestGCSStorage.
#[tokio::test(flavor = "multi_thread")]
async fn gcs_storage_exists_get_put() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/storage/v1/b/bkt/o/pref%2Ftest-key"))
        .and(query_param("alt", "media"))
        .and(header("Authorization", "Bearer token"))
        .respond_with(ResponseTemplate::new(200).set_body_string("tarball-bytes"))
        .mount(&srv)
        .await;
    Mock::given(method("GET"))
        .and(path("/storage/v1/b/bkt/o/pref%2Ftest-key"))
        .and(header("Authorization", "Bearer token"))
        .respond_with(ResponseTemplate::new(200).set_body_string(r#"{"name":"pref/test-key"}"#))
        .mount(&srv)
        .await;
    Mock::given(method("GET"))
        .and(path("/storage/v1/b/bkt/o/pref%2Fmissing-key"))
        .respond_with(ResponseTemplate::new(404))
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/upload/storage/v1/b/bkt/o"))
        .and(query_param("uploadType", "media"))
        .and(query_param("name", "pref/test-key"))
        .and(header("Content-Type", "application/octet-stream"))
        .and(wiremock::matchers::body_string("tarball-bytes"))
        .respond_with(ResponseTemplate::new(200))
        .expect(1)
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(400))
        .mount(&srv)
        .await;
    let url = srv.uri();
    blocking(move || {
        let g = GcsStorage::with_base_url(Box::new(|| Ok("token".into())), "bkt", "pref", &url)
            .unwrap();
        assert!(g.exists("test-key").unwrap());
        assert!(!g.exists("missing-key").unwrap());
        assert_eq!(read_all(g.get("test-key").unwrap()), b"tarball-bytes");
        assert_eq!(g.get("missing-key").err().unwrap(), "gcs get status: 404");
        g.put("test-key", b"tarball-bytes".to_vec()).unwrap();
        assert_eq!(
            g.put("other", b"x".to_vec()).unwrap_err(),
            "gcs put status: 400"
        );
    })
    .await;
}

#[tokio::test(flavor = "multi_thread")]
async fn gcs_storage_other_statuses_are_errors() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .respond_with(ResponseTemplate::new(403))
        .mount(&srv)
        .await;
    let url = srv.uri();
    let err = blocking(move || {
        GcsStorage::with_base_url(Box::new(|| Ok("t".into())), "b", "", &url)
            .unwrap()
            .exists("k")
            .unwrap_err()
    })
    .await;
    assert_eq!(err, "gcs status: 403");
}

// Go: TestNewGCSStorageUsesGCSBaseURL and TestGCSBaseURL (the trailing slash).
#[test]
fn base_urls() {
    let g = GcsStorage::new(Box::new(|| Ok(String::new())), "b", "p").unwrap();
    assert_eq!(g.base_url(), crate::gcs_base_url());
    let g = GcsStorage::with_base_url(
        Box::new(|| Ok(String::new())),
        "b",
        "p",
        "http://127.0.0.1:9999/",
    )
    .unwrap();
    assert_eq!(g.base_url(), "http://127.0.0.1:9999");
}

// Go: TestRegistryFetch.
#[tokio::test(flavor = "multi_thread")]
async fn registry_download_url() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path(format!(
            "/v1/providers/hashicorp/google/6.10.0/download/{}/{}",
            goos(),
            goarch()
        )))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_string(r#"{"download_url": "http://example.com/binary.zip"}"#),
        )
        .mount(&srv)
        .await;
    let url = srv.uri();
    let got = blocking(move || {
        RegistryClient::with_base_url("registry.terraform.io", &url)
            .unwrap()
            .download_url(&Provider {
                address: "hashicorp/google".into(),
                version: "6.10.0".into(),
            })
    })
    .await;
    assert_eq!(got.unwrap(), "http://example.com/binary.zip");
}

// Go: TestFetchFromRegistry.
#[tokio::test(flavor = "multi_thread")]
async fn registry_fetch() {
    let bin = MockServer::start().await;
    Mock::given(method("GET"))
        .respond_with(ResponseTemplate::new(200).set_body_string("mock binary content"))
        .mount(&bin)
        .await;
    let reg = MockServer::start().await;
    Mock::given(method("GET"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_string(format!(r#"{{"download_url": "{}/zip"}}"#, bin.uri())),
        )
        .mount(&reg)
        .await;
    let url = reg.uri();
    let (body, len) = blocking(move || {
        let (b, len) = RegistryClient::with_base_url("registry.terraform.io", &url)
            .unwrap()
            .fetch(&Provider {
                address: "hashicorp/google".into(),
                version: "6.10.0".into(),
            })
            .unwrap();
        (read_all(b), len)
    })
    .await;
    assert_eq!(body, b"mock binary content");
    assert_eq!(len, Some(19));
}

#[tokio::test(flavor = "multi_thread")]
async fn registry_errors() {
    let reg = MockServer::start().await;
    Mock::given(method("GET"))
        .respond_with(ResponseTemplate::new(404))
        .mount(&reg)
        .await;
    let url = reg.uri();
    let err = blocking(move || {
        RegistryClient::with_base_url("registry.terraform.io", &url)
            .unwrap()
            .download_url(&Provider {
                address: "registry.terraform.io/hashicorp/null".into(),
                version: "1".into(),
            })
            .unwrap_err()
    })
    .await;
    assert_eq!(err, "registry status: 404");
}
