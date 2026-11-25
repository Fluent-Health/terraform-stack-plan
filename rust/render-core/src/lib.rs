//! The pure core of `tfstackplan render`: Terraform plan JSON and the HCL policy in, markdown
//! (and the classification sidecar) out. No file system, clock or network: the binary reads the
//! inputs and writes the outputs.
//!
//! Each module is a port of the Go package of the same name under `internal/`, and produces the
//! same bytes. The `go*` modules and `yaml` reproduce the Go standard library and `yaml.v3`
//! behaviour the output depends on.

#![forbid(unsafe_code)]

pub mod ansi;
pub mod classify;
pub mod codes;
pub mod config;
pub mod differ;
pub mod difflib;
pub mod fit;
pub mod gofmt;
pub mod gojson;
pub mod goregex;
pub mod goval;
mod isprint;
pub mod links;
pub mod model;
pub mod moveset;
pub mod pipeline;
pub mod plan;
pub mod render;
pub mod source;
pub mod yaml;

pub use pipeline::{Builder, Output};
