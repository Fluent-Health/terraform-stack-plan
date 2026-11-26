//! Helpers for reading the JSON Go writes.

use serde::{Deserialize, Deserializer, Serialize, Serializer};

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

/// Writes an empty slice or map as `null`, as Go encodes the nil one. Go's shell
/// leaves these nil unless a value arrives, so an empty one is (almost always) nil
/// on the Go side; a Go empty-but-non-nil value cannot be told apart once decoded.
pub(crate) fn null_if_empty<S, T>(v: &T, serializer: S) -> Result<S::Ok, S::Error>
where
    S: Serializer,
    T: Serialize + IsEmpty,
{
    if v.is_empty() {
        serializer.serialize_none()
    } else {
        v.serialize(serializer)
    }
}

/// Collections [`null_if_empty`] can check.
pub(crate) trait IsEmpty {
    fn is_empty(&self) -> bool;
}

impl<T: IsEmpty> IsEmpty for &T {
    fn is_empty(&self) -> bool {
        T::is_empty(self)
    }
}

impl<T> IsEmpty for Vec<T> {
    fn is_empty(&self) -> bool {
        Vec::is_empty(self)
    }
}

impl<K, V> IsEmpty for std::collections::BTreeMap<K, V> {
    fn is_empty(&self) -> bool {
        std::collections::BTreeMap::is_empty(self)
    }
}
