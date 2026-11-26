use super::OAuth;

fn client(auth_url: &str) -> OAuth {
    OAuth {
        client_id: "cid".into(),
        client_secret: "sec".into(),
        auth_url: auth_url.into(),
        token_url: "http://127.0.0.1:9/token".into(),
        redirect_url: "https://ui.example.com/auth/callback".into(),
        scopes: vec!["openid".into(), "email".into()],
    }
}

#[test]
fn auth_code_url_like_go() {
    assert_eq!(
        client("https://a.example/auth")
            .auth_code_url("s t", &[("include_granted_scopes", "true")]),
        "https://a.example/auth?client_id=cid&include_granted_scopes=true&redirect_uri=https%3A%2F%2Fui.example.com%2Fauth%2Fcallback&response_type=code&scope=openid+email&state=s+t"
    );
    assert!(
        client("https://a.example/auth?x=1")
            .auth_code_url("", &[])
            .starts_with("https://a.example/auth?x=1&client_id=cid&")
    );
}
