//! The encrypted session cookie (Go: `internal/ui/session.go`): AES-256-GCM under a key
//! derived from the configured secret with SHA-256, a random 96-bit nonce, and
//! `base64url(nonce || ciphertext || tag)` without padding. The plaintext is the same JSON
//! Go writes, so a cookie sealed by either implementation opens in the other, and a secret
//! rotation invalidates every session in both.

use aws_lc_rs::aead::{AES_256_GCM, Aad, LessSafeKey, NONCE_LEN, Nonce, UnboundKey};
use aws_lc_rs::digest::{SHA256, digest};
use base64::Engine as _;
use base64::alphabet::URL_SAFE;
use base64::engine::{DecodePaddingMode, GeneralPurpose, GeneralPurposeConfig};
use jiff::{SignedDuration, Timestamp};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

/// The browser session cookie name (also the security scheme in api/ui.openapi.yaml).
pub const SESSION_COOKIE: &str = "tfsp_ui_session";

/// How long a login lasts: seven days.
pub const SESSION_TTL: SignedDuration = SignedDuration::from_hours(7 * 24);

/// Go's `base64.RawURLEncoding` decoder: no padding, and non-zero trailing bits accepted.
const B64: GeneralPurpose = GeneralPurpose::new(
    &URL_SAFE,
    GeneralPurposeConfig::new()
        .with_encode_padding(false)
        .with_decode_allow_trailing_bits(true)
        .with_decode_padding_mode(DecodePaddingMode::RequireNone),
);

/// The identity a session carries; no tokens.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Session {
    /// The verified, lowercased Google email.
    #[serde(default)]
    pub email: String,
    /// When the session stops opening.
    pub expires: Timestamp,
}

/// Seals and opens session tokens (Go's `sessionCodec`).
pub struct Codec {
    key: LessSafeKey,
}

impl std::fmt::Debug for Codec {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Codec")
    }
}

impl Codec {
    /// A codec keyed by SHA-256 of `secret`, which must not be empty.
    pub fn new(secret: &str) -> Result<Codec, String> {
        if secret.is_empty() {
            return Err("session secret is empty".into());
        }
        let key = digest(&SHA256, secret.as_bytes());
        let key = UnboundKey::new(&AES_256_GCM, key.as_ref()).map_err(|e| e.to_string())?;
        Ok(Codec {
            key: LessSafeKey::new(key),
        })
    }

    /// Encrypts `v`'s JSON into a cookie-safe token (Go's `sealJSON`).
    pub fn seal_json<T: Serialize>(&self, v: &T) -> Result<String, String> {
        let mut buf = serde_json::to_vec(v).map_err(|e| e.to_string())?;
        let mut nonce = [0u8; NONCE_LEN];
        aws_lc_rs::rand::fill(&mut nonce).map_err(|e| e.to_string())?;
        self.key
            .seal_in_place_append_tag(Nonce::assume_unique_for_key(nonce), Aad::empty(), &mut buf)
            .map_err(|e| e.to_string())?;
        let mut out = nonce.to_vec();
        out.extend_from_slice(&buf);
        Ok(B64.encode(out))
    }

    /// Decrypts a token into `T` (Go's `openJSON`). Authenticity comes from the seal;
    /// expiry is the payload's concern. Errors read like Go's.
    pub fn open_json<T: DeserializeOwned>(&self, token: &str) -> Result<T, String> {
        // Go's decoder skips CR and LF anywhere in the input.
        let clean: String = token
            .chars()
            .filter(|c| !matches!(c, '\r' | '\n'))
            .collect();
        let mut raw = B64
            .decode(clean)
            .map_err(|e| format!("token decode: {e}"))?;
        if raw.len() < NONCE_LEN {
            return Err("token too short".into());
        }
        let mut ct = raw.split_off(NONCE_LEN);
        let nonce = Nonce::try_assume_unique_for_key(&raw).map_err(|e| e.to_string())?;
        let Ok(plain) = self.key.open_in_place(nonce, Aad::empty(), &mut ct) else {
            return Err("token open: cipher: message authentication failed".into());
        };
        serde_json::from_slice(plain).map_err(|e| e.to_string())
    }

    /// Seals a session into a cookie value.
    pub fn seal(&self, s: &Session) -> Result<String, String> {
        self.seal_json(s)
    }

    /// Opens a session token, rejecting expired sessions.
    pub fn open(&self, token: &str) -> Result<Session, String> {
        let s: Session = self.open_json(token).map_err(|e| format!("session: {e}"))?;
        if Timestamp::now() > s.expires {
            return Err("session expired".into());
        }
        Ok(s)
    }
}

#[cfg(test)]
mod tests;
