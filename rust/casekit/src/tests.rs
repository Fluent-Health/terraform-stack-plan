use super::*;

#[test]
fn harness_table_round_trips() {
    let doc: toml_edit::DocumentMut = r#"
bin.name = "tfstackplan"
args = ["claims", "list"]

[harness]
requires = ["terraform"]
env = { TFSTACKPLAN_SERVER = "{fake}" }
adc = "authorized_user"

[[harness.route]]
method = "POST"
path = "/api/claims/list"
body = '[]'
"#
    .parse()
    .unwrap();
    let h = Harness::from_case(&doc).unwrap();
    assert_eq!(h.requires, vec!["terraform"]);
    assert_eq!(h.adc.as_deref(), Some("authorized_user"));
    assert_eq!(h.routes.len(), 1);
    assert_eq!(h.routes[0].status, 200);

    let s = h.start().unwrap();
    let url = s.fake_url().unwrap();
    assert!(
        s.env
            .contains(&("TFSTACKPLAN_SERVER".to_string(), url.clone()))
    );
    assert!(
        s.home()
            .join(".config/gcloud/application_default_credentials.json")
            .is_file()
    );
    assert_eq!(s.redact(&format!("at {url}/x")), "at [FAKE]/x");
}

#[test]
fn fake_server_answers_routes_and_404s() {
    let srv = FakeServer::start(vec![Route {
        method: "POST".into(),
        path: "/a".into(),
        status: 500,
        body: "boom".into(),
    }])
    .unwrap();
    let call = |req: &str| {
        let mut c = TcpStream::connect(srv.addr).unwrap();
        c.write_all(req.as_bytes()).unwrap();
        let mut out = String::new();
        c.read_to_string(&mut out).unwrap();
        out
    };
    let r = call("POST /a?x=1 HTTP/1.1\r\nContent-Length: 2\r\n\r\n{}");
    assert!(r.starts_with("HTTP/1.1 500 "), "{r}");
    assert!(r.ends_with("boom"));
    let r = call("GET /b HTTP/1.1\r\n\r\n");
    assert!(r.starts_with("HTTP/1.1 404 "), "{r}");
}

#[test]
fn no_harness_means_no_server() {
    let doc: toml_edit::DocumentMut = "args = []\n".parse().unwrap();
    let h = Harness::from_case(&doc).unwrap();
    let s = h.start().unwrap();
    assert!(s.fake_url().is_none());
    assert!(
        s.env
            .iter()
            .any(|(k, v)| k == "TFSTACKPLAN_SERVER" && v.is_empty())
    );
}

#[test]
fn reserved_keys_cover_env_add_and_harness_env() {
    for k in CREDENTIAL_ENV {
        assert!(HERMETIC_UNSET.contains(k), "{k} is not blanked");
    }
    assert_eq!(reserved_key([("HOME", "/tmp/x")]), Some("HOME"));
    assert_eq!(
        reserved_key([
            ("TFSTACKPLAN_SERVER", "{fake}"),
            ("TFSTACKPLAN_GO_BIN", "/bin/true")
        ]),
        Some("TFSTACKPLAN_GO_BIN")
    );
    assert_eq!(
        reserved_key([("GOOGLE_APPLICATION_CREDENTIALS", "/home/me/key.json")]),
        Some("GOOGLE_APPLICATION_CREDENTIALS")
    );
    // The value must start with the placeholder, not just contain it.
    assert_eq!(
        reserved_key([("GOOGLE_APPLICATION_CREDENTIALS", "/home/me/{fake}.json")]),
        Some("GOOGLE_APPLICATION_CREDENTIALS")
    );
    assert_eq!(reserved_key([("GCE_METADATA_HOST", "{fakehost}")]), None);
    assert_eq!(
        reserved_key([("TFSTACKPLAN_SERVER", "https://x.example")]),
        None
    );

    // from_case runs the check over `[harness] env` and `env.add`, with or
    // without a `[harness]` table.
    let err = |toml: &str| Harness::from_case(&toml.parse().unwrap()).unwrap_err();
    assert_eq!(
        err("[harness]\nenv = { HOME = \"/root\" }\n"),
        "harness.env must not set HOME: the recorder and the replay set it"
    );
    assert_eq!(
        err("[env]\nadd = { TFSTACKPLAN_FALLBACK_LOG = \"/tmp/log\" }\n"),
        "env.add must not set TFSTACKPLAN_FALLBACK_LOG: the recorder and the replay set it"
    );
    assert_eq!(
        err("[env]\nadd = { CLOUDSDK_CONFIG = \"/home/me/.config/gcloud\" }\n"),
        "env.add may set CLOUDSDK_CONFIG only to the fake server (a value starting with {fake} or {fakehost})"
    );
}
