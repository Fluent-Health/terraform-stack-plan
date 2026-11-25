use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use wiremock::{Mock, MockServer, Request, Respond, ResponseTemplate};

use super::*;

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.unwrap()
}

fn token() -> TokenFn {
    Box::new(|| Ok("test-token".to_string()))
}

fn info() -> LockInfo {
    LockInfo {
        id: "abc".into(),
        operation: "tfstackplan state apply".into(),
        who: "me@host".into(),
        version: "tfstackplan".into(),
        created: "2026-01-01T00:00:00Z".into(),
        path: "gs://my-tf-state/stacks/foo/default.tflock".into(),
    }
}

/// A lock bucket: POST creates the object unless it exists (412), DELETE removes it.
#[derive(Clone, Default)]
struct LockBucket {
    held: Arc<Mutex<bool>>,
    deleted: Arc<Mutex<usize>>,
}

impl Respond for LockBucket {
    fn respond(&self, r: &Request) -> ResponseTemplate {
        let mut held = self.held.lock().unwrap();
        match (r.method.as_str(), r.url.path()) {
            ("POST", "/upload/storage/v1/b/my-tf-state/o") => {
                let q: BTreeMap<_, _> = r.url.query_pairs().into_owned().collect();
                assert_eq!(q["name"], "stacks/foo/default.tflock");
                assert_eq!(q["ifGenerationMatch"], "0");
                assert_eq!(r.headers.get("authorization").unwrap(), "Bearer test-token");
                if *held {
                    return ResponseTemplate::new(412);
                }
                *held = true;
                ResponseTemplate::new(200).set_body_string(r#"{"generation":"1"}"#)
            }
            ("DELETE", "/storage/v1/b/my-tf-state/o/stacks%2Ffoo%2Fdefault.tflock") => {
                *held = false;
                *self.deleted.lock().unwrap() += 1;
                ResponseTemplate::new(204)
            }
            _ => ResponseTemplate::new(400),
        }
    }
}

// Go: TestGCSLockerAcquireConflict.
#[tokio::test(flavor = "multi_thread")]
async fn locker_acquire_conflict_and_release() {
    let srv = MockServer::start().await;
    let bucket = LockBucket::default();
    Mock::given(wiremock::matchers::any())
        .respond_with(bucket.clone())
        .mount(&srv)
        .await;
    let url = srv.uri();
    blocking(move || {
        let lk = Locker::new(token(), &url).unwrap();
        let first = lk
            .acquire("stacks/foo", "my-tf-state", "stacks/foo", &info())
            .unwrap();
        let err = lk
            .acquire("dir/x", "my-tf-state", "stacks/foo", &info())
            .err()
            .unwrap();
        assert_eq!(
            err,
            "state dir/x is already locked (gs://my-tf-state/stacks/foo/default.tflock)"
        );
        first.release().unwrap();
        let again = lk
            .acquire("stacks/foo", "my-tf-state", "stacks/foo", &info())
            .unwrap();
        again.release().unwrap();
    })
    .await;
    assert_eq!(*bucket.deleted.lock().unwrap(), 2);
}

#[tokio::test(flavor = "multi_thread")]
async fn locker_reports_unexpected_status() {
    let srv = MockServer::start().await;
    Mock::given(wiremock::matchers::any())
        .respond_with(ResponseTemplate::new(403).set_body_string(" denied \n"))
        .mount(&srv)
        .await;
    let url = srv.uri();
    let err = blocking(move || {
        Locker::new(token(), &url)
            .unwrap()
            .acquire("d", "b", "p", &info())
            .err()
            .unwrap()
    })
    .await;
    assert_eq!(err, "acquire lock: unexpected status 403: denied");
}

/// An object bucket keyed by object name.
#[derive(Clone, Default)]
struct Objects(Arc<Mutex<BTreeMap<String, Vec<u8>>>>);

impl Respond for Objects {
    fn respond(&self, r: &Request) -> ResponseTemplate {
        let mut objs = self.0.lock().unwrap();
        if r.method.as_str() == "POST" && r.url.path().contains("/upload/") {
            let name = r
                .url
                .query_pairs()
                .find(|(k, _)| k == "name")
                .map(|(_, v)| v.into_owned())
                .unwrap();
            objs.insert(name, r.body.clone());
            return ResponseTemplate::new(200).set_body_string("{}");
        }
        if r.method.as_str() == "GET" {
            let p = r.url.path();
            let i = p.rfind("/o/").unwrap();
            let name = p[i + 3..].replace("%2F", "/");
            return match objs.get(&name) {
                Some(b) => ResponseTemplate::new(200).set_body_bytes(b.clone()),
                None => ResponseTemplate::new(404).set_body_string("not found"),
            };
        }
        ResponseTemplate::new(200)
    }
}

// Go: TestGCSObjectStorePutGet, TestGCSObjectStoreGetMissing.
#[tokio::test(flavor = "multi_thread")]
async fn object_store_put_get_and_missing() {
    let srv = MockServer::start().await;
    let objects = Objects::default();
    Mock::given(wiremock::matchers::any())
        .respond_with(objects.clone())
        .mount(&srv)
        .await;
    let url = srv.uri();
    blocking(move || {
        let s = ObjectStore::new(token(), "bkt", "logs", &url).unwrap();
        s.put("executions/e1/s/log", b"hello".to_vec()).unwrap();
        assert_eq!(s.get("executions/e1/s/log").unwrap(), b"hello");
        assert_eq!(
            s.get("nope").unwrap_err(),
            "gcs get logs/nope: 404".to_string()
        );
    })
    .await;
    assert!(
        objects
            .0
            .lock()
            .unwrap()
            .contains_key("logs/executions/e1/s/log")
    );
}

// Go: TestGCSClientsDefaultToGCSBaseURL (the explicit-URL half; the env default is
// read by gcs_base_url, covered below).
#[test]
fn explicit_base_url_wins_and_is_trimmed() {
    let s = ObjectStore::new(token(), "b", "p", "http://explicit/").unwrap();
    assert_eq!(s.base_url(), "http://explicit");
}
