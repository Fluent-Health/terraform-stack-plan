use std::sync::Mutex;

use super::*;

#[derive(Default)]
struct Rec {
    chunks: Mutex<Vec<(String, String)>>,
    fail: Mutex<bool>,
}

impl Poster for Rec {
    fn post(&self, c: &LogChunk) -> Result<(), String> {
        if *self.fail.lock().map_err(|e| e.to_string())? {
            return Err("down".into());
        }
        self.chunks
            .lock()
            .map_err(|e| e.to_string())?
            .push((c.stack.clone(), c.data.clone()));
        Ok(())
    }
}

// Go: TestLogPumpDeltaOffset.
#[test]
fn posts_only_new_bytes_and_retries_failures() {
    let root = tempfile::tempdir().unwrap();
    let dir = root.path().join("stacks/a");
    std::fs::create_dir_all(&dir).unwrap();
    let log = dir.join("tfstackplan.log");
    let rec = Rec::default();
    let r = root.path().to_string_lossy().into_owned();
    let mut p = Pump::new(&rec, &r, "tfstackplan.log", "e1");
    p.pump("stacks/a"); // no file yet
    std::fs::write(&log, "one\n").unwrap();
    p.pump("stacks/a");
    *rec.fail.lock().unwrap() = true;
    std::fs::write(&log, "one\ntwo\n").unwrap();
    p.pump("stacks/a");
    *rec.fail.lock().unwrap() = false;
    p.pump("stacks/a");
    p.pump("stacks/a");
    assert_eq!(
        *rec.chunks.lock().unwrap(),
        vec![
            ("stacks/a".to_string(), "one\n".to_string()),
            ("stacks/a".to_string(), "two\n".to_string()),
        ]
    );
}

// Go: TestLogPumpStartStopFlush.
#[test]
fn final_flush_after_work() {
    let root = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(root.path().join("s")).unwrap();
    let rec = Rec::default();
    let r = root.path().to_string_lossy().into_owned();
    let stacks = vec!["s".to_string()];
    let got = while_pumping(
        Some(Pump::new(&rec, &r, "f.log", "e1")),
        &stacks,
        Duration::from_secs(3600),
        || {
            std::fs::write(root.path().join("s/f.log"), "late\n").unwrap();
            7
        },
    );
    assert_eq!(got, 7);
    assert_eq!(*rec.chunks.lock().unwrap(), vec![("s".to_string(), "late\n".to_string())]);
    assert_eq!(while_pumping(None, &stacks, INTERVAL, || 1), 1);
}
