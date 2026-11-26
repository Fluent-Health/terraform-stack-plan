//! Helpers for reading the JSON Go writes.

use serde::{Deserialize, Deserializer};

/// Reads `null` as the type's default. Go encodes a nil slice or map as `null`, which
/// serde would otherwise reject for a `Vec` or a map. Use with `#[serde(default)]`
/// so an absent field reads the same way.
pub(crate) fn nullable<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

/// For `skip_serializing_if`: Go's `omitempty` on an integer.
pub(crate) fn is_zero(n: &i64) -> bool {
    *n == 0
}
