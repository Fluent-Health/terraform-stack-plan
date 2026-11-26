//! The decisions inside `run wrap`: which status a wrapped command's outcome
//! reports, how output is batched into log chunks, and the capped tail kept for
//! classification.

use std::sync::LazyLock;

use regex::Regex;

static APPLY_COMPLETE: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    crate::goregex::compile(
        r"Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed",
    )
    .expect("valid apply-summary pattern")
});

/// Every stack status the server knows (`events.Status`).
pub const STATUSES: &[&str] = &[
    "pending",
    "running",
    "initializing",
    "initialized",
    "planned",
    "gated",
    "safe",
    "nochange",
    "moving",
    "failed",
    "aborted",
];

/// Whether `s` is a known stack status (guards `--running` against typos).
pub fn known_status(s: &str) -> bool {
    STATUSES.contains(&s)
}

/// The status to report for a wrapped command (Go's `classifyOutcome`), given its
/// exit code, its colour-stripped output and `--on-success` ("" when unset):
/// non-zero exit is `failed`; an apply summary of 0/0/0 is `nochange`; any other
/// apply summary is `safe` unless `--on-success` says otherwise; no summary is
/// `--on-success` (possibly "", meaning report nothing).
pub fn classify_outcome(exit_code: i32, output: &str, on_success: &str) -> String {
    if exit_code != 0 {
        return "failed".to_string();
    }
    if let Some(m) = APPLY_COMPLETE.captures(output) {
        if &m[1] == "0" && &m[2] == "0" && &m[3] == "0" {
            return "nochange".to_string();
        }
        if on_success.is_empty() {
            return "safe".to_string();
        }
    }
    on_success.to_string()
}

/// The last non-blank line, trimmed: the failure detail sent with `failed`.
pub fn last_line(s: &str) -> String {
    s.trim_end_matches('\n')
        .split('\n')
        .rev()
        .map(str::trim)
        .find(|l| !l.is_empty())
        .unwrap_or_default()
        .to_string()
}

/// Bytes a log chunk collects before it is posted.
pub const FLUSH_THRESHOLD: usize = 4 << 10;

/// Batches output into log chunks of at least [`FLUSH_THRESHOLD`] bytes (Go's
/// `logStreamer`). Writes return the chunk to post, if one is ready.
#[derive(Debug, Default)]
pub struct LogBatcher {
    buf: Vec<u8>,
}

impl LogBatcher {
    /// Adds output; returns a chunk once the buffer reaches the threshold.
    pub fn write(&mut self, p: &[u8]) -> Option<String> {
        self.buf.extend_from_slice(p);
        if self.buf.len() >= FLUSH_THRESHOLD {
            return self.flush();
        }
        None
    }

    /// Whatever is buffered, as a final chunk.
    pub fn flush(&mut self) -> Option<String> {
        if self.buf.is_empty() {
            return None;
        }
        let chunk = String::from_utf8_lossy(&self.buf).into_owned();
        self.buf.clear();
        Some(chunk)
    }
}

/// The capture kept for classification: the last 64 KiB (the apply summary is at the tail).
pub const CAPTURE_MAX: usize = 64 << 10;

/// Output captured for classification, capped to its last [`CAPTURE_MAX`] bytes.
#[derive(Debug, Default)]
pub struct Capture {
    buf: Vec<u8>,
}

impl Capture {
    /// Appends output, dropping the oldest bytes past the cap.
    pub fn write(&mut self, p: &[u8]) {
        self.buf.extend_from_slice(p);
        if self.buf.len() > CAPTURE_MAX {
            let cut = self.buf.len() - CAPTURE_MAX;
            self.buf.drain(..cut);
        }
    }

    /// The captured text.
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.buf).into_owned()
    }
}

#[cfg(test)]
mod tests;
