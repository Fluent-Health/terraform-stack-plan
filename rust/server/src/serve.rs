//! The `serve` subcommand (Go: `cmd/tfstackplan/serve.go`, `internal/catalog`):
//! configuration, the backends, the background loops, graceful shutdown.

use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;

use crate::{App, http};

/// GET /api/catalog: the component catalog of the working directory, built from
/// `terramate list`.
pub(crate) async fn catalog(_app: Arc<App>) -> Response {
    match crate::catalog::build(".").await {
        Ok(c) => http::json_ok(&c),
        Err(e) => http::error(
            StatusCode::INTERNAL_SERVER_ERROR,
            &format!("build catalog: {e}"),
        ),
    }
}
