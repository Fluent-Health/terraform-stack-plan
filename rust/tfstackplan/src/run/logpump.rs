//! Go's `runner.LogPump`: tails each stack's on-disk log file while a terramate
//! script runs and posts the bytes appended since the last post. Every post is
//! best effort; a failed post is retried on the next tick.

use std::collections::BTreeMap;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;
use std::sync::mpsc;
use std::time::Duration;

use tfstackplan_clients::server::LogChunk;

/// Something that posts a log chunk (the server client).
pub trait Poster: Sync {
    /// Posts one chunk.
    fn post(&self, chunk: &LogChunk) -> Result<(), String>;
}

impl Poster for tfstackplan_clients::server::Client {
    fn post(&self, chunk: &LogChunk) -> Result<(), String> {
        self.log_chunk(chunk)
    }
}

/// The pump's state: bytes already posted, per stack.
pub struct Pump<'a> {
    poster: &'a dyn Poster,
    root: &'a str,
    file: &'a str,
    exec_id: &'a str,
    offsets: BTreeMap<String, u64>,
}

impl<'a> Pump<'a> {
    /// A pump posting through `poster`, reading `<root>/<stack>/<file>`.
    pub fn new(poster: &'a dyn Poster, root: &'a str, file: &'a str, exec_id: &'a str) -> Self {
        Self {
            poster,
            root,
            file,
            exec_id,
            offsets: BTreeMap::new(),
        }
    }

    /// Posts what one stack's log gained since the last successful post.
    pub fn pump(&mut self, stack: &str) {
        let path = Path::new(self.root).join(stack).join(self.file);
        let Ok(mut f) = std::fs::File::open(path) else {
            return;
        };
        let off = self.offsets.get(stack).copied().unwrap_or(0);
        if f.seek(SeekFrom::Start(off)).is_err() {
            return;
        }
        let mut data = Vec::new();
        if f.read_to_end(&mut data).is_err() || data.is_empty() {
            return;
        }
        let chunk = LogChunk {
            id: self.exec_id.to_string(),
            stack: stack.to_string(),
            data: String::from_utf8_lossy(&data).into_owned(),
        };
        if self.poster.post(&chunk).is_ok() {
            self.offsets.insert(stack.to_string(), off + data.len() as u64);
        }
    }

    /// Pumps every stack once.
    pub fn pump_all(&mut self, stacks: &[String]) {
        for s in stacks {
            self.pump(s);
        }
    }
}

/// How often the pump polls.
pub const INTERVAL: Duration = Duration::from_secs(2);

/// Runs `work` while a pump tails the stacks' logs every `interval`, then flushes
/// once more and returns `work`'s result (Go's `LogPump.Start` and its stop func).
/// With `pump` `None`, just runs `work`.
pub fn while_pumping<R>(
    pump: Option<Pump<'_>>,
    stacks: &[String],
    interval: Duration,
    work: impl FnOnce() -> R,
) -> R {
    let Some(mut pump) = pump else {
        return work();
    };
    let (stop, stopped) = mpsc::channel::<()>();
    std::thread::scope(|s| {
        s.spawn(move || {
            while let Err(mpsc::RecvTimeoutError::Timeout) = stopped.recv_timeout(interval) {
                pump.pump_all(stacks);
            }
            pump.pump_all(stacks);
        });
        let r = work();
        let _ = stop.send(());
        r
    })
}

#[cfg(test)]
mod tests;
