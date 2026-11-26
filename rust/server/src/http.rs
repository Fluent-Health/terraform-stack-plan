//! Responses shaped like Go's `net/http` helpers.

use axum::body::Body;
use axum::http::{HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use serde::Serialize;

use crate::gojson;

/// Go's `http.Error`: text/plain, nosniff, the message and a newline.
pub fn error(status: StatusCode, msg: &str) -> Response {
    let mut r = Response::new(Body::from(format!("{msg}\n")));
    *r.status_mut() = status;
    let h = r.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    h.insert(
        header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    r
}

/// `w.WriteHeader(status)` with no body and no content type.
pub fn status(status: StatusCode) -> Response {
    status.into_response()
}

/// A 200 with no body.
pub fn ok() -> Response {
    status(StatusCode::OK)
}

/// `w.Header().Set("Content-Type", "application/json")` and Go's encoder.
pub fn json<T: Serialize + ?Sized>(status: StatusCode, v: &T) -> Response {
    let mut r = Response::new(Body::from(gojson::encode(v)));
    *r.status_mut() = status;
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    r
}

/// A 200 JSON response.
pub fn json_ok<T: Serialize + ?Sized>(v: &T) -> Response {
    json(StatusCode::OK, v)
}

/// The stable `{code, message}` error the runner switches on.
pub fn coded_error(status: StatusCode, code: &str, msg: &str) -> Response {
    #[derive(Serialize)]
    struct Coded<'a> {
        code: &'a str,
        message: &'a str,
    }
    json(status, &Coded { code, message: msg })
}

/// Go's `badRequest`: the decode error's text with 400.
pub fn bad_request(msg: &str) -> Response {
    error(StatusCode::BAD_REQUEST, msg)
}

/// Go's `http.NotFound`.
pub fn not_found_page() -> Response {
    error(StatusCode::NOT_FOUND, "404 page not found")
}
