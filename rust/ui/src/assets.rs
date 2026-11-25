//! The embedded SPA (Go: `internal/ui/assets.go`). The repository commits a placeholder
//! `internal/ui/dist/index.html`; release builds overwrite `dist/` with the Vite output
//! before compiling, so a shipped binary carries the real SPA.
//!
//! As in Go: an existing asset path is served the way `http.FileServer` serves it (content
//! type by extension, else sniffed; `/index.html` and directories redirect), and every
//! other path gets `index.html` for client-side routing.

use axum::http::{HeaderMap, header};
use rust_embed::RustEmbed;

use crate::gohttp::{self, Resp};

#[derive(RustEmbed)]
#[folder = "../../internal/ui/dist"]
struct Dist;

/// Go's `mime.TypeByExtension` built-in table (extensions are matched lowercased).
fn type_by_extension(name: &str) -> Option<&'static str> {
    let ext = name.rsplit_once('.').map(|(_, e)| e.to_ascii_lowercase())?;
    Some(match ext.as_str() {
        "avif" => "image/avif",
        "css" => "text/css; charset=utf-8",
        "gif" => "image/gif",
        "htm" | "html" => "text/html; charset=utf-8",
        "jpeg" | "jpg" => "image/jpeg",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "json" => "application/json",
        "pdf" => "application/pdf",
        "png" => "image/png",
        "svg" => "image/svg+xml",
        "wasm" => "application/wasm",
        "webp" => "image/webp",
        "xml" => "text/xml; charset=utf-8",
        _ => return None,
    })
}

/// Go's `fs.ValidPath`.
fn valid_path(p: &str) -> bool {
    if p == "." {
        return true;
    }
    !p.is_empty() && p.split('/').all(|e| !e.is_empty() && e != "." && e != "..")
}

fn is_dir(p: &str) -> bool {
    p == "." || Dist::iter().any(|f| f.starts_with(&format!("{p}/")))
}

/// Serves the SPA for `GET /` and every path no other route claims.
pub(crate) fn serve(raw_path: &str, raw_query: &str, h: &HeaderMap) -> Resp {
    // Go works on the unescaped URL path here.
    let path =
        String::from_utf8_lossy(&gohttp::lenient_path_unescape(raw_path.as_bytes())).into_owned();
    let p = path.strip_prefix('/').unwrap_or(&path);
    if !p.is_empty() && valid_path(p) {
        if let Some(file) = Dist::get(p) {
            return file_server(&path, raw_query, h, p, &file.data);
        }
        if is_dir(p) {
            // FileServer redirects a directory without its trailing slash.
            return local_redirect(&format!("{}/", gohttp::path_base(&path)), raw_query);
        }
    }
    match Dist::get("index.html") {
        Some(index) => gohttp::html_bytes(index.data.into_owned()),
        None => gohttp::error(500, "ui assets missing"),
    }
}

/// `http.FileServer` for one embedded file.
fn file_server(url_path: &str, raw_query: &str, h: &HeaderMap, name: &str, data: &[u8]) -> Resp {
    if url_path.ends_with("/index.html") {
        return local_redirect("./", raw_query);
    }
    // No ETag and no modification time on embedded files, so the only precondition that
    // can hold is `If-None-Match: *`.
    if h.get(header::IF_NONE_MATCH)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|v| v.trim_start().starts_with('*'))
    {
        return gohttp::status(304, axum::body::Body::empty());
    }
    let ct = type_by_extension(name).unwrap_or_else(|| gohttp::sniff(data));
    let mut r = gohttp::status(200, data.to_vec());
    let hm = r.headers_mut();
    gohttp::set_header(hm, "accept-ranges", "bytes");
    gohttp::set_header(hm, "content-type", ct);
    gohttp::set_header(hm, "content-length", &data.len().to_string());
    r
}

/// net/http's `localRedirect`: a 301 with only a Location.
fn local_redirect(new_path: &str, raw_query: &str) -> Resp {
    let loc = if raw_query.is_empty() {
        new_path.to_string()
    } else {
        format!("{new_path}?{raw_query}")
    };
    let mut r = gohttp::status(301, axum::body::Body::empty());
    gohttp::set_header(r.headers_mut(), "location", &loc);
    r
}

#[cfg(test)]
mod tests;
