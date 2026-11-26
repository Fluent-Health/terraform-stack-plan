use crate::session::SESSION_COOKIE;
use crate::testutil::{get, req, send, session_app};

// Go: TestPublicSurfaceAndSPA.
#[tokio::test]
async fn public_surface_and_spa() {
    let (h, sess) = session_app(vec![]);
    assert_eq!(
        get(&h, "/healthz", None).await.status,
        200,
        "/healthz should be public"
    );
    assert_eq!(get(&h, "/ready", None).await.status, 200);
    // The SPA shell serves at / and at client-routed paths without a session.
    for p in ["/", "/pr/7", "/tiers/nonprod/executions/e1"] {
        let rr = get(&h, p, None).await;
        assert!(
            rr.status == 200 && rr.body.contains("tfstackplan ui"),
            "SPA fallback {p}: {}",
            rr.status
        );
        assert_eq!(rr.header("content-type"), "text/html; charset=utf-8");
    }
    // Logout clears the session cookie.
    let rr = send(&h, req("POST", "/auth/logout", Some(&sess))).await;
    assert_eq!(rr.status, 204);
    assert_eq!(
        rr.set_cookies(),
        vec![format!(
            "{SESSION_COOKIE}=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax"
        )]
    );
}

#[tokio::test]
async fn file_server_redirects_index_and_dirs() {
    let (h, _) = session_app(vec![]);
    let rr = get(&h, "/index.html?x=1", None).await;
    assert_eq!((rr.status, rr.header("location")), (301, "./?x=1"));
    assert_eq!(rr.header("content-type"), "");
    // "/%2E" escapes the mux's cleaning and opens the root directory.
    let rr = get(&h, "/%2E", None).await;
    assert_eq!((rr.status, rr.header("location")), (301, "./"));
    // Paths fs.ValidPath rejects fall back to the shell.
    let rr = get(&h, "/%2E%2E", None).await;
    assert_eq!(rr.status, 200);
}
