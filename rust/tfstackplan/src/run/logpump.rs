//! Go's `runner.LogPump`: tails each stack's on-disk log file while a terramate
//! script runs and posts the bytes appended since the last post. Every post is
//! best effort; a failed post is retried on the next tick.

use std::collections::BTreeMap;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;
use std::sync::mpsc;
use std::time::{Duration, Instant};

use tfstackplan_clients::server::LogChunk;

/// Something that posts a log chunk (the server client).
pub trait Poster: Sync {
    /// Posts one chunk, within `timeout` when given (else the poster's default).
    fn post(&self, chunk: &LogChunk, timeout: Option<Duration>) -> Result<(), String>;
}

impl Poster for tfstackplan_clients::server::Client {
    fn post(&self, chunk: &LogChunk, timeout: Option<Duration>) -> Result<(), String> {
        match timeout {
            Some(t) => self.log_chunk_within(chunk, t),
            None => self.log_chunk(chunk),
        }
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
        self.pump_within(stack, None);
    }

    /// [`Pump::pump`] with the post bounded by `timeout`.
    fn pump_within(&mut self, stack: &str, timeout: Option<Duration>) {
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
        if self.poster.post(&chunk, timeout).is_ok() {
            self.offsets
                .insert(stack.to_string(), off + data.len() as u64);
        }
    }

    /// One tick: pumps every stack, stopping early (and returning when it was
    /// told to stop) once `stopped` fires, as Go's cancelled context ends an
    /// in-flight tick.
    fn tick(&mut self, stacks: &[String], stopped: &mpsc::Receiver<Instant>) -> Option<Instant> {
        for s in stacks {
            match stopped.try_recv() {
                Ok(at) => return Some(at),
                Err(mpsc::TryRecvError::Disconnected) => return Some(Instant::now()),
                Err(mpsc::TryRecvError::Empty) => self.pump(s),
            }
        }
        None
    }

    /// The final flush: every stack, all within one [`FLUSH_TIMEOUT`] counted
    /// from `since` (Go's single 10 s flush context).
    fn flush(&mut self, stacks: &[String], since: Instant) {
        let deadline = since + FLUSH_TIMEOUT;
        for s in stacks {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return;
            }
            self.pump_within(s, Some(left));
        }
    }
}

/// How often the pump polls.
pub const INTERVAL: Duration = Duration::from_secs(2);

/// The final flush's total budget, counted from the stop.
pub const FLUSH_TIMEOUT: Duration = Duration::from_secs(10);

/// Runs `work` while a pump tails the stacks' logs every `interval`, then flushes
/// once more and returns `work`'s result (Go's `LogPump.Start` and its stop func).
/// The stop ends an in-flight tick between stacks, and the final flush gets one
/// [`FLUSH_TIMEOUT`] in total from the stop, so a blackholed server cannot stall
/// the end of the run. With `pump` `None`, just runs `work`.
pub fn while_pumping<R>(
    pump: Option<Pump<'_>>,
    stacks: &[String],
    interval: Duration,
    work: impl FnOnce() -> R,
) -> R {
    let Some(mut pump) = pump else {
        return work();
    };
    let (stop, stopped) = mpsc::channel::<Instant>();
    std::thread::scope(|s| {
        s.spawn(move || {
            let since = loop {
                match stopped.recv_timeout(interval) {
                    Ok(at) => break at,
                    Err(mpsc::RecvTimeoutError::Disconnected) => break Instant::now(),
                    Err(mpsc::RecvTimeoutError::Timeout) => {}
                }
                if let Some(at) = pump.tick(stacks, &stopped) {
                    break at;
                }
            };
            pump.flush(stacks, since);
        });
        let r = work();
        let _ = stop.send(Instant::now());
        r
    })
}

#[cfg(test)]
mod tests;
