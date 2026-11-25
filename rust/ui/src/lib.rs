//! `tfstackplan ui`, the central UI service, ported from Go (`internal/ui`, the generated
//! `internal/uiapi` router, `internal/config/ui.go`, `cmd/tfstackplan/ui.go`): a stateless
//! aggregator over the tier serves with Google login for humans (an encrypted session
//! cookie; the SPA never sees a token) and Google OIDC service identity toward the tiers.
//!
//! | Go | module |
//! |---|---|
//! | `app.go`, `internal/uiapi` | [`app`] |
//! | `auth.go` | `auth` |
//! | `session.go` | [`session`] |
//! | `proxy.go`, `streams.go` | `proxy` |
//! | `approve.go` | `approve` |
//! | `ghrelay.go` | `ghrelay` |
//! | `assets.go` | `assets` |
//! | `internal/config/ui.go` | [`config`] |
//! | `cmd/tfstackplan/ui.go` | [`cli`] |
#![forbid(unsafe_code)]

pub mod app;
mod approve;
mod assets;
mod auth;
pub mod cli;
pub mod config;
mod ghrelay;
mod gohttp;
pub mod oauth;
mod proxy;
pub mod session;

pub use app::{App, Config, Tier};
pub use cli::run;
#[cfg(test)]
mod testutil;
