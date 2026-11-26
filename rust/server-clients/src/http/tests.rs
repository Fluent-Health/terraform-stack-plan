use std::time::Duration;

use super::*;

#[test]
fn building_the_client_does_not_panic() {
    install_crypto_provider();
    install_crypto_provider();
    client(Duration::from_secs(5)).unwrap();
    client(Duration::from_secs(60)).unwrap();
}

#[test]
fn endpoint_path_drops_host_and_query() {
    assert_eq!(
        endpoint_path("https://api.github.com/repos/o/r/pulls/7?x=tok"),
        "/repos/o/r/pulls/7"
    );
    assert_eq!(endpoint_path("http://127.0.0.1:9/graphql#f"), "/graphql");
    assert_eq!(endpoint_path("https://host"), "/");
}

#[test]
fn escapes_match_go() {
    assert_eq!(query_escape("logs/a b+c"), "logs%2Fa+b%2Bc");
    assert_eq!(path_escape("my bucket/x"), "my%20bucket%2Fx");
    assert_eq!(path_escape("a+b:c@d"), "a+b:c@d");
}
