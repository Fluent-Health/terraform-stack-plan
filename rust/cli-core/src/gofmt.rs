//! Go's text formatting where the CLI output depends on it, all from
//! `tfstackplan_render::gofmt`: `strconv.Quote` (what `%q` prints), `unicode.IsPrint`,
//! `FormatFloat('f', -1)`, and `format_v`, the `%v` form of a float64 (`%g` with the
//! shortest digits).

pub use tfstackplan_render::gofmt::{format_f, format_g as format_v, is_print, quote};

#[cfg(test)]
mod tests;
