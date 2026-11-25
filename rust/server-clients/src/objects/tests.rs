use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use super::*;
use crate::testserver::{Resp, serve, unescape};

fn tok() -> TokenFunc {
    Arc::new(|| async { Ok::<_, Error>("t".to_string()) })
}

#[tokio::test]
async fn gcs_object_store_put_get() {
    let objects = Arc::new(Mutex::new(BTreeMap::<String, Vec<u8>>::new()));
    let o = Arc::clone(&objects);
    let srv = serve(move |r| {
        let mut objects = o.lock().unwrap();
        if r.method == "POST" && r.path.contains("/upload/") {
            objects.insert(r.param("name", false), r.body.clone());
            Resp::ok("{}")
        } else if r.method == "GET" {
            let i = r.path.rfind("/o/").unwrap();
            match objects.get(&unescape(&r.path[i + 3..])) {
                Some(b) => Resp::ok(b.clone()),
                None => Resp::status(404, "not found"),
            }
        } else {
            Resp::ok("")
        }
    })
    .await
    .unwrap();
    let s = Gcs::new(tok(), "bkt", "logs", &srv.url).unwrap();
    s.put("executions/e1/s/log", b"hello".to_vec())
        .await
        .unwrap();
    assert!(
        objects
            .lock()
            .unwrap()
            .contains_key("logs/executions/e1/s/log")
    );
    assert_eq!(s.get("executions/e1/s/log").await.unwrap(), b"hello");
    let err = s.get("nope").await.unwrap_err();
    assert_eq!(err, Error::NotFound("gcs get logs/nope: 404".into()));
}

#[tokio::test]
async fn gcs_put_sets_content_length_and_auth() {
    let seen = Arc::new(Mutex::new(None));
    let s2 = Arc::clone(&seen);
    let srv = serve(move |r| {
        *s2.lock().unwrap() = Some(r);
        Resp::ok("")
    })
    .await
    .unwrap();
    let content = b"terraform apply line\n".repeat(1000);
    let s = Gcs::new(tok(), "b", "p", &srv.url).unwrap();
    s.put("k", content.clone()).await.unwrap();
    let r = seen.lock().unwrap().clone().unwrap();
    assert_eq!(r.body, content);
    assert_eq!(r.header("content-length"), content.len().to_string());
    assert_eq!(r.header("authorization"), "Bearer t");
    assert_eq!(r.header("content-type"), "application/octet-stream");
    assert_eq!(r.path, "/upload/storage/v1/b/b/o");
    assert_eq!(r.query, "uploadType=media&name=p%2Fk");
}

#[tokio::test]
async fn gcs_put_error_text() {
    let srv = serve(|_| Resp::status(403, "denied")).await.unwrap();
    let s = Gcs::new(tok(), "b", "", &srv.url).unwrap();
    assert_eq!(
        s.put("k", b"x".to_vec()).await.unwrap_err().to_string(),
        "gcs put k: 403: denied"
    );
    let srv = serve(|_| Resp::status(500, "")).await.unwrap();
    let s = Gcs::new(tok(), "b", "", &srv.url).unwrap();
    assert_eq!(s.get("k").await.unwrap_err(), Error::msg("gcs get k: 500"));
}

#[test]
fn gcs_clients_default_to_gcs_base_url() {
    assert_eq!(gcs_base_url_from(None), "https://storage.googleapis.com");
    assert_eq!(
        gcs_base_url_from(Some("http://fake-gcs/")),
        "http://fake-gcs"
    );
    let s = Gcs::new(tok(), "b", "p", "").unwrap();
    assert_eq!(s.base_url(), gcs_base_url());
    let s = Gcs::new(tok(), "b", "p", "http://explicit/").unwrap();
    assert_eq!(s.base_url(), "http://explicit");
}
