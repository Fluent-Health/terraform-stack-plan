//! `tfstackplan ui` as a standalone binary, for the Go-versus-Rust HTTP replay
//! (`tests/replay/replay.py`) until the front door routes `ui` to Rust.

#[tokio::main]
async fn main() -> std::process::ExitCode {
    tfstackplan_ui::run(std::env::args().skip(1).collect()).await
}
