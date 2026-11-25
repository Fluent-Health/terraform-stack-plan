//! Per-stack log buffers (Go: `logs.go`, `objects.go`): ingestion, offload to the
//! object store, the static and following reads, and the `/plan` fragment.

use std::convert::Infallible;
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::Response;
use tfstackplan_gate::events::LogChunk;
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};
use tokio::sync::mpsc;
use tracing::warn;

use crate::gojson::shape;
use crate::ingest::decode_or_400;
use crate::render::{exec_kind, is_finished};
use crate::{App, Error, Result, http, markdown};

/// The tail size mirrored into the store for instant render.
const EXCERPT_BYTES: u64 = 16 << 10;

/// Reduces an untrusted path component to a flat token that cannot traverse.
pub(crate) fn sanitize_component(s: &str) -> String {
    s.replace(['/', '\\'], "_").replace("..", "_")
}

/// The object-store key of a stack's log.
pub(crate) fn object_key(exec: &str, stack: &str) -> String {
    format!(
        "executions/{}/{}/log",
        sanitize_component(exec),
        sanitize_component(stack)
    )
}

/// Go's `filepath.Clean` for the containment check.
fn clean(p: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for c in p.components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => {
                if !out.pop() {
                    out.push("..");
                }
            }
            other => out.push(other.as_os_str()),
        }
    }
    out
}

/// The buffer path of (exec, stack) under `base`, if it is safely inside it.
pub(crate) fn log_file_path(base: &str, exec: &str, stack: &str) -> Option<PathBuf> {
    if base.is_empty() {
        return None;
    }
    let p = Path::new(base)
        .join(sanitize_component(exec))
        .join(format!("{}.log", sanitize_component(stack)));
    let clean_base = clean(Path::new(base));
    let clean_p = clean(&p);
    (clean_p.starts_with(&clean_base) && clean_p != clean_base).then_some(p)
}

/// The last `n` bytes of a file.
async fn tail_file(path: &Path, n: u64) -> std::io::Result<String> {
    let mut f = tokio::fs::File::open(path).await?;
    let size = f.metadata().await?.len();
    let start = size.saturating_sub(n);
    f.seek(std::io::SeekFrom::Start(start)).await?;
    let mut b = Vec::with_capacity(usize::try_from(size - start).unwrap_or(0));
    f.read_to_end(&mut b).await?;
    Ok(String::from_utf8_lossy(&b).into_owned())
}

impl App {
    /// Appends a chunk to the stack's buffer and mirrors the tail into the store.
    async fn append_log(&self, exec_id: &str, stack: &str, data: &str) -> Result<()> {
        let Some(p) = log_file_path(&self.cfg.logs_dir, exec_id, stack) else {
            return Ok(());
        };
        let io = |e: std::io::Error| Error::Message(e.to_string());
        if let Some(dir) = p.parent() {
            tokio::fs::create_dir_all(dir).await.map_err(io)?;
        }
        let mut f = tokio::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&p)
            .await
            .map_err(io)?;
        f.write_all(data.as_bytes()).await.map_err(io)?;
        drop(f);
        let excerpt = tail_file(&p, EXCERPT_BYTES).await.map_err(io)?;
        let (id, st) = (exec_id.to_string(), stack.to_string());
        self.db(move |s| s.upsert_stack_output(&id, &st, "log", "", &excerpt))
            .await?;
        self.hub.publish(&format!("{exec_id}|{stack}"), data);
        Ok(())
    }

    /// Uploads a stack's buffer to the object store and records the key. `Err`
    /// without a store (so a buffer is never deleted unoffloaded); `Ok` when there
    /// was no buffer.
    pub(crate) async fn offload_log(&self, exec_id: &str, stack: &str) -> Result<()> {
        let Some(objects) = &self.objects else {
            return Err(Error::Message("no object store".into()));
        };
        let Some(p) = log_file_path(&self.cfg.logs_dir, exec_id, stack) else {
            return Ok(());
        };
        let Ok(body) = tokio::fs::read(&p).await else {
            return Ok(());
        };
        let key = object_key(exec_id, stack);
        if let Err(e) = objects.put(&key, body).await {
            warn!("offload log {exec_id}/{stack}: {e}");
            return Err(Error::Message(e.to_string()));
        }
        let (id, st) = (exec_id.to_string(), stack.to_string());
        let res = self
            .db(move |s| {
                let excerpt = s
                    .get_stack_output(&id, &st, "log")?
                    .map(|(_, e)| e)
                    .unwrap_or_default();
                s.upsert_stack_output(&id, &st, "log", &key, &excerpt)
            })
            .await;
        if let Err(e) = &res {
            warn!("offload log pointer {exec_id}/{stack}: {e}");
        }
        res
    }

    /// Offloads every stack's buffer of an execution and, when all succeeded,
    /// deletes the execution's buffer directory.
    pub(crate) async fn finalize_logs_for(&self, exec_id: &str) {
        let id = exec_id.to_string();
        let Ok(g) = self.db(move |s| s.load_graph(&id)).await else {
            return;
        };
        let mut ok = true;
        for st in &g.stacks {
            if self.offload_log(exec_id, &st.path).await.is_err() {
                ok = false;
            }
        }
        if !ok || self.cfg.logs_dir.is_empty() {
            return;
        }
        let dir = Path::new(&self.cfg.logs_dir).join(sanitize_component(exec_id));
        if let Err(e) = tokio::fs::remove_dir_all(&dir).await
            && e.kind() != std::io::ErrorKind::NotFound
        {
            warn!("clean log buffers {exec_id}: {e}");
        }
    }

    /// Removes execution buffer directories older than `max_age` (orphans left by
    /// a restart mid-execution).
    pub async fn clean_log_buffers(&self, max_age: Duration) {
        if self.cfg.logs_dir.is_empty() {
            return;
        }
        let Ok(mut entries) = tokio::fs::read_dir(&self.cfg.logs_dir).await else {
            return;
        };
        let cutoff = std::time::SystemTime::now()
            .checked_sub(max_age)
            .unwrap_or(std::time::UNIX_EPOCH);
        while let Ok(Some(e)) = entries.next_entry().await {
            let Ok(meta) = e.metadata().await else {
                continue;
            };
            let Ok(modified) = meta.modified() else {
                continue;
            };
            if modified >= cutoff {
                continue;
            }
            let path = e.path();
            let res = if meta.is_dir() {
                tokio::fs::remove_dir_all(&path).await
            } else {
                tokio::fs::remove_file(&path).await
            };
            if let Err(err) = res {
                warn!(
                    "clean log buffers: {}: {err}",
                    e.file_name().to_string_lossy()
                );
            }
        }
    }
}

/// POST /api/logs: one per-stack output chunk.
pub(crate) async fn append(app: Arc<App>, body: &[u8]) -> Response {
    let c: LogChunk = decode_or_400!(body, shape::LOG_CHUNK);
    if app.append_log(&c.id, &c.stack, &c.data).await.is_err() {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "append log");
    }
    http::ok()
}

fn text_plain(body: Vec<u8>) -> Response {
    let mut r = Response::new(Body::from(body));
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    r
}

/// GET /logs/{exec}/{stack...}: the buffer, else the offloaded object; with
/// `?follow=` an SSE stream.
pub(crate) async fn serve(
    app: Arc<App>,
    exec: String,
    stack: String,
    follow: bool,
    headers: &HeaderMap,
) -> Response {
    if follow {
        return stream(app, exec, stack, headers);
    }
    if let Some(p) = log_file_path(&app.cfg.logs_dir, &exec, &stack)
        && let Ok(b) = tokio::fs::read(&p).await
    {
        return text_plain(b);
    }
    if let Some(objects) = &app.objects {
        let (e, st) = (exec.clone(), stack.clone());
        if let Ok(Some((pointer, _))) = app.db(move |s| s.get_stack_output(&e, &st, "log")).await
            && !pointer.is_empty()
            && let Ok(b) = objects.get(&pointer).await
        {
            return text_plain(b);
        }
    }
    http::error(StatusCode::NOT_FOUND, "not found")
}

/// GET /plan/{exec}/{stack...}: one stack's stored plan section as HTML; an empty
/// body when none is stored.
pub(crate) async fn plan(app: Arc<App>, exec: String, stack: String) -> Response {
    let md = app
        .db(move |s| s.get_stack_output(&exec, &stack, "plan"))
        .await
        .ok()
        .flatten()
        .map(|(_, md)| md)
        .unwrap_or_default();
    let mut r = Response::new(Body::from(if md.is_empty() {
        String::new()
    } else {
        markdown::render(&md)
    }));
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/html; charset=utf-8"),
    );
    r
}

/// One SSE event: each line on its own `data:` field.
pub(crate) fn sse(data: &str) -> String {
    let mut out = String::new();
    for line in data.strip_suffix('\n').unwrap_or(data).split('\n') {
        out.push_str("data: ");
        out.push_str(line);
        out.push('\n');
    }
    out.push('\n');
    out
}

fn sse_event(id: u64, data: &str) -> String {
    format!("id: {id}\n{}", sse(data))
}

const SSE_DONE: &str = "event: done\ndata: \n\n";
const SSE_PING: &str = ": ping\n\n";
/// The heartbeat interval of every SSE stream.
pub(crate) const PING_EVERY: Duration = Duration::from_secs(25);

/// An SSE response whose body is fed by `run` through the sender. The task ends
/// when the client goes away (a send fails).
pub(crate) fn sse_response<F, Fut>(run: F) -> Response
where
    F: FnOnce(mpsc::Sender<Result<Bytes, Infallible>>) -> Fut,
    Fut: Future<Output = ()> + Send + 'static,
{
    let (tx, rx) = mpsc::channel(16);
    tokio::spawn(run(tx));
    let mut r = Response::new(Body::from_stream(
        tokio_stream::wrappers::ReceiverStream::new(rx),
    ));
    let h = r.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/event-stream"),
    );
    h.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
    h.insert(header::CONNECTION, HeaderValue::from_static("keep-alive"));
    r
}

/// Sends one chunk; `false` when the client is gone.
pub(crate) async fn send(tx: &mpsc::Sender<Result<Bytes, Infallible>>, s: String) -> bool {
    tx.send(Ok(Bytes::from(s))).await.is_ok()
}

fn stream(app: Arc<App>, exec: String, stack: String, headers: &HeaderMap) -> Response {
    let mut offset: u64 = headers
        .get("Last-Event-ID")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.trim().parse::<i64>().ok())
        .and_then(|n| u64::try_from(n).ok())
        .unwrap_or(0);
    sse_response(move |tx| async move {
        if app.finished(&exec).await {
            app.stream_offloaded(&tx, &exec, &stack, &mut offset).await;
            let _ = send(&tx, SSE_DONE.into()).await;
            return;
        }
        let mut chunks = app.hub.subscribe(&format!("{exec}|{stack}"));
        let mut changes = app.hub.subscribe(&format!("exec:{exec}"));
        let mut served_buffer = false;
        if let Some(p) = log_file_path(&app.cfg.logs_dir, &exec, &stack)
            && let Ok(mut f) = tokio::fs::File::open(&p).await
        {
            served_buffer = true;
            if f.seek(std::io::SeekFrom::Start(offset)).await.is_ok() {
                let mut buf = vec![0u8; 32 << 10];
                while let Ok(n) = f.read(&mut buf).await {
                    if n == 0 {
                        break;
                    }
                    offset += n as u64;
                    if !send(&tx, sse_event(offset, &String::from_utf8_lossy(&buf[..n]))).await {
                        return;
                    }
                }
            }
        }
        if !served_buffer && app.stream_offloaded(&tx, &exec, &stack, &mut offset).await {
            let _ = send(&tx, SSE_DONE.into()).await;
            return;
        }
        let mut ping =
            tokio::time::interval_at(tokio::time::Instant::now() + PING_EVERY, PING_EVERY);
        loop {
            tokio::select! {
                () = tx.closed() => return,
                Some(chunk) = chunks.rx.recv() => {
                    offset += chunk.len() as u64;
                    if !send(&tx, sse_event(offset, &chunk)).await {
                        return;
                    }
                }
                Some(_) = changes.rx.recv() => {
                    if app.finished(&exec).await {
                        app.stream_offloaded(&tx, &exec, &stack, &mut offset).await;
                        let _ = send(&tx, SSE_DONE.into()).await;
                        return;
                    }
                }
                _ = ping.tick() => {
                    if !send(&tx, SSE_PING.into()).await {
                        return;
                    }
                }
            }
        }
    })
}

impl App {
    async fn finished(&self, exec: &str) -> bool {
        let id = exec.to_string();
        match self.db(move |s| s.get_execution(&id)).await {
            Ok(Some(e)) => is_finished(exec_kind(&e.status_context), &e.report_markdown, &e.status),
            _ => false,
        }
    }

    /// Replays the offloaded log from `offset`; `false` when there is none yet.
    async fn stream_offloaded(
        &self,
        tx: &mpsc::Sender<Result<Bytes, Infallible>>,
        exec: &str,
        stack: &str,
        offset: &mut u64,
    ) -> bool {
        let Some(objects) = &self.objects else {
            return false;
        };
        let (e, st) = (exec.to_string(), stack.to_string());
        let Ok(Some((pointer, _))) = self.db(move |s| s.get_stack_output(&e, &st, "log")).await
        else {
            return false;
        };
        if pointer.is_empty() {
            return false;
        }
        let Ok(body) = objects.get(&pointer).await else {
            return false;
        };
        let start = usize::try_from(*offset).unwrap_or(usize::MAX);
        if start > body.len() {
            return true;
        }
        for chunk in body[start..].chunks(32 << 10) {
            *offset += chunk.len() as u64;
            if !send(tx, sse_event(*offset, &String::from_utf8_lossy(chunk))).await {
                return true;
            }
        }
        true
    }
}

#[cfg(test)]
mod tests;
