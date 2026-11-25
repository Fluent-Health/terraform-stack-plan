//! The logic behind the `tfstackplan` CLI faces that do not need the renderer:
//! `state`, `claims`, `catalog`, `uniqueness`, `whoami`, `admin` and `run wrap`.
//!
//! Everything here is pure: no filesystem, network, process or clock access, and
//! nothing async. The binary reads files, runs terraform and talks HTTP, then hands
//! the data to these functions and prints what they return. Output that Go writes
//! byte for byte (usage text, HCL shims, JSON) is built here so it can be tested
//! against the Go strings directly.

#![forbid(unsafe_code)]

pub mod ansi;
pub mod catalog;
pub mod claims;
pub mod config;
pub mod flag;
pub mod glob;
pub mod gofmt;
pub mod gojson;
pub mod goregex;
pub mod gourl;
pub mod hclx;
pub mod jwt;
pub mod statemove;
pub mod uniqueness;
pub mod wrap;
