//! The pure parts of the terraform provider cache (Go's `internal/cache`): reading
//! `.terraform.lock.hcl`, naming cache objects and provider directories, and
//! building and decoding registry download requests.

use crate::hclx::{self, Decoder};

/// One provider pinned in a lock file.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Provider {
    /// `registry.terraform.io/hashicorp/google`.
    pub address: String,
    /// `6.10.0`.
    pub version: String,
}

/// Parses a `.terraform.lock.hcl` (Go's `parseLockFileContent`): every
/// `provider "<address>" { version = … }` block. Errors start `hcl parse:` or
/// `hcl decode:` as in Go; the diagnostic text after it is hcl-rs's.
pub fn parse_lock_file(src: &str) -> Result<Vec<Provider>, String> {
    let body = hclx::parse(src).map_err(|e| format!("hcl parse: {e}"))?;
    let decode = |e: String| format!("hcl decode: {e}");
    let mut d = Decoder::new(&body);
    let mut out = Vec::new();
    for blk in d.blocks("provider") {
        let labels = hclx::labels(blk);
        if labels.len() != 1 {
            return Err(decode(
                "Missing name for provider; All provider blocks must have 1 labels (address)."
                    .to_string(),
            ));
        }
        let mut b = Decoder::new(&blk.body);
        let version = b.req_str("version").map_err(decode)?;
        b.opt_str("constraints").map_err(decode)?;
        b.opt_strings("hashes").map_err(decode)?;
        b.finish().map_err(decode)?;
        out.push(Provider {
            address: labels[0].clone(),
            version,
        });
    }
    d.finish().map_err(decode)?;
    Ok(out)
}

/// Go's `runtime.GOOS` for this build.
pub fn goos() -> &'static str {
    match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    }
}

/// Go's `runtime.GOARCH` for this build.
pub fn goarch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        "x86" => "386",
        "powerpc64" => "ppc64",
        "loongarch64" => "loong64",
        other => other,
    }
}

/// `GOOS_GOARCH`, the platform directory terraform's plugin cache uses.
pub fn platform() -> String {
    format!("{}_{}", goos(), goarch())
}

/// The cache object for a provider: `[<version>/]<address>/<pversion>/<platform>.tar.gz`.
/// A cache version of `""` or `"0"` adds no prefix.
pub fn object_key(cache_version: &str, address: &str, version: &str, platform: &str) -> String {
    let key = format!("{address}/{version}/{platform}.tar.gz");
    if cache_version.is_empty() || cache_version == "0" {
        key
    } else {
        format!("{cache_version}/{key}")
    }
}

/// The provider's directory under the cache dir: `<address>/<version>/<platform>`.
pub fn provider_dir(p: &Provider, platform: &str) -> String {
    format!("{}/{}/{platform}", p.address, p.version)
}

/// `terraform-provider-<name>` for an address `…/<name>`.
pub fn binary_name(address: &str) -> String {
    let base = address.rsplit('/').next().unwrap_or(address);
    format!("terraform-provider-{base}")
}

/// For a provider dir found under the cache dir (`rel` = `<address…>/<version>/<platform>`,
/// slash-separated), the address and version, or `None` when it has fewer than four parts.
pub fn address_version_of(rel: &str) -> Option<(String, String)> {
    let parts: Vec<&str> = rel.split('/').collect();
    if parts.len() < 4 {
        return None;
    }
    let version = parts[parts.len() - 2].to_string();
    let address = parts[..parts.len() - 2].join("/");
    Some((address, version))
}

/// The registry download API URL for a provider (Go's `GetDownloadURL`). A
/// three-part address whose host is `host` uses `base_url` (a test server can
/// stand in); another host is called directly over https; a two-part address
/// (`namespace/name`) uses `base_url`.
pub fn download_api_url(
    host: &str,
    base_url: &str,
    p: &Provider,
    goos: &str,
    goarch: &str,
) -> Result<String, String> {
    let parts: Vec<&str> = p.address.split('/').collect();
    let (api_base, ns, name) = if parts.len() >= 3 {
        let base = if parts[0] == host {
            base_url.to_string()
        } else {
            format!("https://{}", parts[0])
        };
        (base, parts[parts.len() - 2], parts[parts.len() - 1])
    } else if parts.len() == 2 {
        (base_url.to_string(), parts[0], parts[1])
    } else {
        return Err(format!("invalid provider address: {}", p.address));
    };
    Ok(format!(
        "{api_base}/v1/providers/{ns}/{name}/{}/download/{goos}/{goarch}",
        p.version
    ))
}

/// The `download_url` of a registry download response.
pub fn decode_download_url(body: &[u8]) -> Result<String, String> {
    #[derive(serde::Deserialize)]
    struct Payload {
        #[serde(default)]
        download_url: String,
    }
    serde_json::from_slice::<Payload>(body)
        .map(|p| p.download_url)
        .map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests;
