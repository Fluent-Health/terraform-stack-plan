//! Reading the identity out of an ID token, as `whoami` and the server client's
//! auth-failure hint do (`decodeJWTSubject`). The signature is not checked: this
//! only names who the token says it is.

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;

/// The token's `email` claim, else its `sub`. Errors mirror Go's messages for a
/// malformed token.
pub fn subject(token: &str) -> Result<String, String> {
    let parts: Vec<&str> = token.split('.').collect();
    if parts.len() < 2 {
        return Err("invalid token format".to_string());
    }
    let payload = URL_SAFE_NO_PAD
        .decode(parts[1])
        .map_err(|e| format!("illegal base64 data at input byte {}", corrupt_at(&e)))?;
    let claims: serde_json::Value = serde_json::from_slice(&payload).map_err(|e| e.to_string())?;
    let field = |k: &str| {
        claims
            .get(k)
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string()
    };
    let email = field("email");
    if !email.is_empty() {
        return Ok(email);
    }
    Ok(field("sub"))
}

/// Where Go's `base64.CorruptInputError` would point for this error.
fn corrupt_at(e: &base64::DecodeError) -> usize {
    match e {
        base64::DecodeError::InvalidByte(at, _) | base64::DecodeError::InvalidLastSymbol(at, _) => {
            *at
        }
        base64::DecodeError::InvalidLength(len) => *len,
        base64::DecodeError::InvalidPadding => 0,
    }
}

#[cfg(test)]
mod tests;
