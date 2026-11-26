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
