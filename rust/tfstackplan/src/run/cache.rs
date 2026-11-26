//! The terraform provider plugin cache (Go's `internal/cache.ProviderCache`):
//! `warm` restores every provider the stacks' lock files pin into the shared
//! plugin cache dir, from the GCS bucket when it has it, else from the registry;
//! `save` uploads the providers the bucket does not have yet.

use std::collections::BTreeMap;
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use tfstackplan_cli::cache::{
    Provider, address_version_of, binary_name, object_key, parse_lock_file, platform,
};
use tfstackplan_clients::cache::{RegistryClient, Storage};

use crate::gofs;

/// The default plugin cache dir when `$TF_PLUGIN_CACHE_DIR` is unset.
const DEFAULT_CACHE_DIR: &str = "/workspace/.tf-plugin-cache";

/// The plugin cache and where it is backed up.
pub struct ProviderCache {
    store: Arc<dyn Storage>,
    registry: Arc<RegistryClient>,
    cache_dir: PathBuf,
    version: String,
    parallelism: usize,
}

/// `ParseLockFile`: reads and parses a lock file.
pub fn parse_lock_file_at(path: &Path) -> Result<Vec<Provider>, String> {
    let src = gofs::read_file(&path.to_string_lossy())?;
    parse_lock_file(&String::from_utf8_lossy(&src))
}

impl ProviderCache {
    /// `NewProviderCache`: an empty `cache_dir` is `$TF_PLUGIN_CACHE_DIR`, else
    /// `/workspace/.tf-plugin-cache`; `version` prefixes object keys unless `""`/`"0"`.
    pub fn new(store: Arc<dyn Storage>, cache_dir: &str, version: &str) -> Result<Self, String> {
        Ok(Self::with_registry(
            store,
            RegistryClient::new()?,
            cache_dir,
            version,
        ))
    }

    /// [`ProviderCache::new`] with an explicit registry client (tests).
    pub fn with_registry(
        store: Arc<dyn Storage>,
        registry: RegistryClient,
        cache_dir: &str,
        version: &str,
    ) -> Self {
        let mut dir = cache_dir.to_string();
        if dir.is_empty() {
            dir = std::env::var("TF_PLUGIN_CACHE_DIR").unwrap_or_default();
        }
        if dir.is_empty() {
            dir = DEFAULT_CACHE_DIR.to_string();
        }
        Self {
            store,
            registry: Arc::new(registry),
            cache_dir: PathBuf::from(dir),
            version: version.to_string(),
            parallelism: 8,
        }
    }

    /// Restores every provider pinned by the stacks' `.terraform.lock.hcl`
    /// (unreadable or unparseable lock files are skipped), 8 at a time. A failed
    /// provider is reported on stderr and counted.
    pub fn warm(&self, stack_paths: &[PathBuf]) -> Result<(), String> {
        fs::create_dir_all(&self.cache_dir)
            .map_err(|e| gofs::path_error("mkdir", &self.cache_dir.to_string_lossy(), &e))?;
        // Go dedupes through a map; the order providers restore in is not observable
        // beyond stderr interleaving.
        let mut unique: BTreeMap<String, Provider> = BTreeMap::new();
        for stack in stack_paths {
            let lock = stack.join(".terraform.lock.hcl");
            if fs::metadata(&lock).is_err() {
                continue;
            }
            let Ok(ps) = parse_lock_file_at(&lock) else {
                continue;
            };
            for p in ps {
                unique.insert(format!("{}/{}", p.address, p.version), p);
            }
        }
        if unique.is_empty() {
            return Ok(());
        }
        let queue = Mutex::new(unique.into_values().collect::<Vec<_>>());
        let errors = AtomicUsize::new(0);
        let workers = self.parallelism.max(1);
        std::thread::scope(|s| {
            for _ in 0..workers {
                s.spawn(|| {
                    loop {
                        let next = queue.lock().ok().and_then(|mut q| q.pop());
                        let Some(p) = next else { break };
                        if let Err(e) = self.restore_one(&p) {
                            let _ = writeln!(
                                io::stderr(),
                                "cache: restore {} {} failed: {e}",
                                p.address,
                                p.version
                            );
                            errors.fetch_add(1, Ordering::SeqCst);
                        }
                    }
                });
            }
        });
        let n = errors.load(Ordering::SeqCst);
        if n > 0 {
            return Err(format!("failed to restore {n} provider(s)"));
        }
        Ok(())
    }

    fn restore_one(&self, p: &Provider) -> Result<(), String> {
        let plat = platform();
        let dest = self.cache_dir.join(&p.address).join(&p.version).join(&plat);
        let bin = binary_name(&p.address);
        if dest.join(&bin).exists() {
            return Ok(()); // already warm
        }
        let key = object_key(&self.version, &p.address, &p.version, &plat);
        let exists = self.store.exists(&key)?;
        fs::create_dir_all(&self.cache_dir).map_err(|e| e.to_string())?;
        let tmp = tempfile::Builder::new()
            .prefix("tmp_cache_")
            .tempdir_in(&self.cache_dir)
            .map_err(|e| e.to_string())?;
        if exists {
            extract_tar_gz(self.store.get(&key)?, tmp.path())?;
        } else {
            let (body, _) = self.registry.fetch(p)?;
            extract_zip(body, tmp.path())?;
        }
        let parent = dest.parent().unwrap_or(&self.cache_dir);
        fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        // An atomic rename into place: concurrent warms are last-writer-wins.
        let tmp_path = tmp.keep();
        let renamed = fs::rename(&tmp_path, &dest);
        let _ = fs::remove_dir_all(&tmp_path);
        if let Err(e) = renamed {
            if dest.join(&bin).exists() {
                return Ok(());
            }
            return Err(format!(
                "rename {} {}: {}",
                tmp_path.display(),
                dest.display(),
                gofs::errno_text(&e)
            ));
        }
        Ok(())
    }

    /// Uploads every provider dir in the cache (a platform dir holding a
    /// `terraform-provider-*` file) that the bucket does not have yet, as a
    /// tar.gz of its files.
    pub fn save(&self) -> Result<(), String> {
        if !self.cache_dir.exists() {
            return Ok(());
        }
        let plat = platform();
        let mut dirs = Vec::new();
        walk(&self.cache_dir, &plat, &mut dirs)?;
        for dir in dirs {
            let Ok(rel) = dir.strip_prefix(&self.cache_dir) else {
                continue;
            };
            let rel = rel.to_string_lossy().replace('\\', "/");
            let Some((address, version)) = address_version_of(&rel) else {
                continue;
            };
            let key = object_key(&self.version, &address, &version, &plat);
            match self.store.exists(&key) {
                Ok(false) => {}
                _ => continue, // already saved, or the check failed
            }
            let data = match tar_gz_dir(&dir) {
                Ok(d) => d,
                Err(e) => {
                    let _ = writeln!(
                        io::stderr(),
                        "cache: save {address} {version}: tar write error: {e}"
                    );
                    continue;
                }
            };
            match self.store.put(&key, data) {
                Ok(()) => {
                    let _ = writeln!(io::stdout(), "plugin-cache: saved {key}");
                }
                Err(e) => {
                    let _ = writeln!(io::stderr(), "cache: save {address} {version} failed: {e}");
                }
            }
        }
        Ok(())
    }
}

/// `filepath.Walk` collecting platform dirs that hold a provider binary.
fn walk(dir: &Path, plat: &str, out: &mut Vec<PathBuf>) -> Result<(), String> {
    let mut entries: Vec<fs::DirEntry> = fs::read_dir(dir)
        .map_err(|e| gofs::path_error("open", &dir.to_string_lossy(), &e))?
        .filter_map(Result::ok)
        .collect();
    entries.sort_by_key(fs::DirEntry::file_name);
    if dir.file_name().is_some_and(|n| n == plat)
        && entries.iter().any(|e| {
            e.file_type().is_ok_and(|t| !t.is_dir())
                && e.file_name()
                    .to_string_lossy()
                    .starts_with("terraform-provider-")
        })
    {
        out.push(dir.to_path_buf());
    }
    for e in entries {
        if e.file_type().is_ok_and(|t| t.is_dir()) {
            walk(&e.path(), plat, out)?;
        }
    }
    Ok(())
}

/// The files (not subdirectories) of `dir` as an in-memory tar.gz, named by file name.
fn tar_gz_dir(dir: &Path) -> Result<Vec<u8>, String> {
    let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
    let mut tw = tar::Builder::new(gz);
    let mut names: Vec<fs::DirEntry> = fs::read_dir(dir)
        .map_err(|e| e.to_string())?
        .filter_map(Result::ok)
        .collect();
    names.sort_by_key(fs::DirEntry::file_name);
    for e in names {
        let path = e.path();
        let Ok(meta) = fs::metadata(&path) else {
            continue;
        };
        if meta.is_dir() {
            continue;
        }
        let mut hdr = tar::Header::new_ustar();
        hdr.set_metadata(&meta);
        let data = fs::read(&path).map_err(|e| e.to_string())?;
        hdr.set_size(data.len() as u64);
        hdr.set_cksum();
        tw.append_data(&mut hdr, e.file_name(), data.as_slice())
            .map_err(|e| e.to_string())?;
    }
    let gz = tw.into_inner().map_err(|e| e.to_string())?;
    gz.finish().map_err(|e| e.to_string())
}

/// Whether `target` (joined under `dest`) stays inside `dest`, as Go checks it:
/// `Clean(target)+"/"` must start with `Clean(dest)+"/"`.
fn inside(dest: &Path, name: &str) -> bool {
    let d = gofs::clean(&dest.to_string_lossy());
    let t = gofs::clean(&format!("{d}/{name}"));
    format!("{t}/").starts_with(&format!("{d}/"))
}

#[cfg(unix)]
fn create(path: &Path, mode: u32) -> io::Result<fs::File> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .create(true)
        .read(true)
        .write(true)
        .mode(mode & 0o7777)
        .open(path)
}

#[cfg(not(unix))]
fn create(path: &Path, _mode: u32) -> io::Result<fs::File> {
    fs::OpenOptions::new()
        .create(true)
        .read(true)
        .write(true)
        .open(path)
}

/// Extracts a tar.gz into `dest`, refusing entries that escape it.
fn extract_tar_gz(r: impl Read, dest: &Path) -> Result<(), String> {
    let mut ar = tar::Archive::new(flate2::read::GzDecoder::new(r));
    for entry in ar.entries().map_err(|e| e.to_string())? {
        let mut entry = entry.map_err(|e| e.to_string())?;
        let name = String::from_utf8_lossy(&entry.path_bytes()).into_owned();
        if !inside(dest, &name) {
            return Err(format!(
                "archive entry {} escapes destination directory",
                tfstackplan_cli::gofmt::quote(&name)
            ));
        }
        let target = dest.join(&name);
        if entry.header().entry_type().is_dir() {
            fs::create_dir_all(&target).map_err(|e| e.to_string())?;
            continue;
        }
        if let Some(parent) = target.parent() {
            fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        let mode = entry.header().mode().unwrap_or(0o644);
        let mut f = create(&target, mode).map_err(|e| e.to_string())?;
        io::copy(&mut entry, &mut f).map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Extracts a zip into `dest` (spooled to a temp file, as Go does), refusing
/// entries that escape it.
fn extract_zip(mut r: impl Read, dest: &Path) -> Result<(), String> {
    let mut spool = tempfile::tempfile().map_err(|e| e.to_string())?;
    io::copy(&mut r, &mut spool).map_err(|e| e.to_string())?;
    let mut zr = zip::ZipArchive::new(spool).map_err(|e| e.to_string())?;
    for i in 0..zr.len() {
        let mut file = zr.by_index(i).map_err(|e| e.to_string())?;
        let name = file.name().map_err(|e| e.to_string())?.into_owned();
        if !inside(dest, &name) {
            return Err(format!(
                "zip entry {} escapes destination directory",
                tfstackplan_cli::gofmt::quote(&name)
            ));
        }
        let path = dest.join(&name);
        if file.is_dir() {
            fs::create_dir_all(&path).map_err(|e| e.to_string())?;
            continue;
        }
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        let mode = file.unix_mode().unwrap_or(0o666);
        let mut f = create(&path, mode).map_err(|e| e.to_string())?;
        io::copy(&mut file, &mut f).map_err(|e| e.to_string())?;
    }
    Ok(())
}

#[cfg(test)]
mod tests;
