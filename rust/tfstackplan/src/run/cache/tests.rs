use std::collections::BTreeMap;
use std::sync::atomic::AtomicUsize;

use tfstackplan_clients::cache::Body;
use wiremock::{Mock, MockServer, ResponseTemplate};

use super::*;

/// An in-memory bucket: objects by key, counting gets.
#[derive(Default)]
struct MockStorage {
    objects: Mutex<BTreeMap<String, Vec<u8>>>,
    gets: AtomicUsize,
}

impl Storage for MockStorage {
    fn exists(&self, key: &str) -> Result<bool, String> {
        let objects = self.objects.lock().map_err(|e| e.to_string())?;
        Ok(objects.contains_key(key))
    }
    fn get(&self, key: &str) -> Result<Body, String> {
        self.gets.fetch_add(1, Ordering::SeqCst);
        let data = self
            .objects
            .lock()
            .map_err(|e| e.to_string())?
            .get(key)
            .cloned()
            .ok_or_else(|| format!("unexpected get key: {key}"))?;
        Ok(Box::new(io::Cursor::new(data)))
    }
    fn put(&self, key: &str, data: Vec<u8>) -> Result<(), String> {
        self.objects
            .lock()
            .map_err(|e| e.to_string())?
            .insert(key.to_string(), data);
        Ok(())
    }
}

fn tar_gz(name: &str, content: &str) -> Vec<u8> {
    let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
    let mut tw = tar::Builder::new(gz);
    let mut hdr = tar::Header::new_ustar();
    hdr.set_mode(0o755);
    hdr.set_size(content.len() as u64);
    hdr.set_cksum();
    tw.append_data(&mut hdr, name, content.as_bytes()).unwrap();
    tw.into_inner().unwrap().finish().unwrap()
}

fn zip_of(name: &str, content: &str) -> Vec<u8> {
    let mut zw = zip::ZipWriter::new(io::Cursor::new(Vec::new()));
    zw.start_file(
        name,
        zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Deflated),
    )
    .unwrap();
    zw.write_all(content.as_bytes()).unwrap();
    zw.finish().unwrap().into_inner()
}

const LOCK: &str = r#"
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 6.0.0"
  hashes = [
    "h1:mockhash",
  ]
}
provider "registry.terraform.io/hashicorp/null" {
  version     = "3.2.1"
  constraints = ">= 3.0.0"
  hashes = [
    "h1:mockhash2",
  ]
}
"#;

fn google_key() -> String {
    format!(
        "v1/registry.terraform.io/hashicorp/google/6.10.0/{}.tar.gz",
        platform()
    )
}

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.unwrap()
}

// Go: TestProviderCacheWarming.
#[tokio::test(flavor = "multi_thread")]
async fn warm_restores_from_storage_and_registry() {
    let zip_srv = MockServer::start().await;
    Mock::given(wiremock::matchers::any())
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_bytes(zip_of("terraform-provider-null", "null-binary-data")),
        )
        .expect(1)
        .mount(&zip_srv)
        .await;
    let reg = MockServer::start().await;
    Mock::given(wiremock::matchers::path(format!(
        "/v1/providers/hashicorp/null/3.2.1/download/{}/{}",
        tfstackplan_cli::cache::goos(),
        tfstackplan_cli::cache::goarch()
    )))
    .respond_with(ResponseTemplate::new(200).set_body_string(format!(
        r#"{{"download_url": "{}/null.zip"}}"#,
        zip_srv.uri()
    )))
    .mount(&reg)
    .await;

    let cache_dir = tempfile::tempdir().unwrap();
    let stack = tempfile::tempdir().unwrap();
    fs::write(stack.path().join(".terraform.lock.hcl"), LOCK).unwrap();
    let store = Arc::new(MockStorage::default());
    store
        .put(
            &google_key(),
            tar_gz("terraform-provider-google", "google-binary-data"),
        )
        .unwrap();

    let (reg_url, dir, stacks, st) = (
        reg.uri(),
        cache_dir.path().to_string_lossy().into_owned(),
        vec![stack.path().to_path_buf()],
        store.clone(),
    );
    blocking(move || {
        let pc = ProviderCache::with_registry(
            st,
            RegistryClient::with_base_url("registry.terraform.io", &reg_url).unwrap(),
            &dir,
            "v1",
        );
        pc.warm(&stacks).unwrap();
    })
    .await;

    assert_eq!(store.gets.load(Ordering::SeqCst), 1);
    let plat = platform();
    let read = |rel: &str| fs::read_to_string(cache_dir.path().join(rel)).unwrap();
    assert_eq!(
        read(&format!(
            "registry.terraform.io/hashicorp/google/6.10.0/{plat}/terraform-provider-google"
        )),
        "google-binary-data"
    );
    assert_eq!(
        read(&format!(
            "registry.terraform.io/hashicorp/null/3.2.1/{plat}/terraform-provider-null"
        )),
        "null-binary-data"
    );
    // Nothing left behind but the providers.
    let leftovers: Vec<_> = fs::read_dir(cache_dir.path())
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| e.file_name().to_string_lossy().starts_with("tmp_cache_"))
        .collect();
    assert!(leftovers.is_empty());
}

#[test]
fn warm_counts_failures() {
    let cache_dir = tempfile::tempdir().unwrap();
    let stack = tempfile::tempdir().unwrap();
    fs::write(stack.path().join(".terraform.lock.hcl"), LOCK).unwrap();
    // Neither provider is stored and the registry is unreachable.
    let pc = ProviderCache::with_registry(
        Arc::new(MockStorage::default()),
        RegistryClient::with_base_url("registry.terraform.io", "http://127.0.0.1:9").unwrap(),
        &cache_dir.path().to_string_lossy(),
        "v1",
    );
    assert_eq!(
        pc.warm(&[stack.path().to_path_buf()]).unwrap_err(),
        "failed to restore 2 provider(s)"
    );
}

#[test]
fn warm_without_lock_files_is_a_noop() {
    let cache_dir = tempfile::tempdir().unwrap();
    let pc = ProviderCache::with_registry(
        Arc::new(MockStorage::default()),
        RegistryClient::with_base_url("registry.terraform.io", "http://127.0.0.1:9").unwrap(),
        &cache_dir.path().join("sub").to_string_lossy(),
        "",
    );
    pc.warm(&[cache_dir.path().join("no-such-stack")]).unwrap();
    assert!(cache_dir.path().join("sub").is_dir());
}

// Go: TestProviderCacheSave.
#[test]
fn save_uploads_missing_providers() {
    let cache_dir = tempfile::tempdir().unwrap();
    let plat = platform();
    let dir = cache_dir
        .path()
        .join(format!("registry.terraform.io/hashicorp/aws/5.0.0/{plat}"));
    fs::create_dir_all(&dir).unwrap();
    fs::write(dir.join("terraform-provider-aws"), "aws-binary-data").unwrap();
    // Already in the bucket: not uploaded again.
    let done = cache_dir
        .path()
        .join(format!("registry.terraform.io/hashicorp/null/1.0.0/{plat}"));
    fs::create_dir_all(&done).unwrap();
    fs::write(done.join("terraform-provider-null"), "x").unwrap();
    let store = Arc::new(MockStorage::default());
    let null_key = format!("v1/registry.terraform.io/hashicorp/null/1.0.0/{plat}.tar.gz");
    store.put(&null_key, b"old".to_vec()).unwrap();

    let pc = ProviderCache::with_registry(
        store.clone(),
        RegistryClient::with_base_url("registry.terraform.io", "http://127.0.0.1:9").unwrap(),
        &cache_dir.path().to_string_lossy(),
        "v1",
    );
    pc.save().unwrap();

    let objects = store.objects.lock().unwrap();
    assert_eq!(objects[&null_key], b"old");
    let aws = &objects[&format!("v1/registry.terraform.io/hashicorp/aws/5.0.0/{plat}.tar.gz")];
    let mut ar = tar::Archive::new(flate2::read::GzDecoder::new(aws.as_slice()));
    let mut found = false;
    for e in ar.entries().unwrap() {
        let mut e = e.unwrap();
        if e.path().unwrap().to_string_lossy() == "terraform-provider-aws" {
            let mut s = String::new();
            e.read_to_string(&mut s).unwrap();
            assert_eq!(s, "aws-binary-data");
            found = true;
        }
    }
    assert!(found, "terraform-provider-aws not in the saved tarball");
}

#[test]
fn save_without_cache_dir_is_a_noop() {
    let tmp = tempfile::tempdir().unwrap();
    let pc = ProviderCache::with_registry(
        Arc::new(MockStorage::default()),
        RegistryClient::with_base_url("registry.terraform.io", "http://127.0.0.1:9").unwrap(),
        &tmp.path().join("absent").to_string_lossy(),
        "v1",
    );
    pc.save().unwrap();
}

// Go: TestProviderCacheWarmConcurrent.
#[test]
fn warm_concurrently_into_one_dir() {
    let cache_dir = tempfile::tempdir().unwrap();
    let stack = tempfile::tempdir().unwrap();
    fs::write(
        stack.path().join(".terraform.lock.hcl"),
        "provider \"registry.terraform.io/hashicorp/google\" {\n  version = \"6.10.0\"\n}\n",
    )
    .unwrap();
    let store = Arc::new(MockStorage::default());
    store
        .put(
            &google_key(),
            tar_gz("terraform-provider-google", "google-binary-data"),
        )
        .unwrap();
    std::thread::scope(|s| {
        for _ in 0..8 {
            s.spawn(|| {
                let pc = ProviderCache::with_registry(
                    store.clone(),
                    RegistryClient::with_base_url("registry.terraform.io", "http://127.0.0.1:9")
                        .unwrap(),
                    &cache_dir.path().to_string_lossy(),
                    "v1",
                );
                pc.warm(&[stack.path().to_path_buf()]).unwrap();
            });
        }
    });
    let bin = cache_dir.path().join(format!(
        "registry.terraform.io/hashicorp/google/6.10.0/{}/terraform-provider-google",
        platform()
    ));
    assert_eq!(fs::read_to_string(bin).unwrap(), "google-binary-data");
    assert!(store.gets.load(Ordering::SeqCst) >= 1);
}

#[test]
fn archives_cannot_escape() {
    let dest = tempfile::tempdir().unwrap();
    let mut gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
    {
        // tar::Builder refuses "..", so write the header by hand.
        let mut hdr = tar::Header::new_gnu();
        hdr.as_gnu_mut().unwrap().name[..12].copy_from_slice(b"../escape.sh");
        hdr.set_size(0);
        hdr.set_mode(0o644);
        hdr.set_cksum();
        gz.write_all(hdr.as_bytes()).unwrap();
        gz.write_all(&[0u8; 1024]).unwrap();
    }
    let err = extract_tar_gz(gz.finish().unwrap().as_slice(), dest.path()).unwrap_err();
    assert_eq!(
        err,
        "archive entry \"../escape.sh\" escapes destination directory"
    );
    let err = extract_zip(io::Cursor::new(zip_of("../x", "y")), dest.path()).unwrap_err();
    assert_eq!(err, "zip entry \"../x\" escapes destination directory");
}

// Go: TestParseLockFile_MissingFile.
#[test]
fn parse_lock_file_missing() {
    assert_eq!(
        parse_lock_file_at(Path::new("nonexistent-file.hcl")).unwrap_err(),
        "open nonexistent-file.hcl: no such file or directory"
    );
}

// Go: TestParseLockFile_RealFile.
#[test]
fn parse_lock_file_real_file() {
    let tmp = tempfile::tempdir().unwrap();
    let p = tmp.path().join(".terraform.lock.hcl");
    fs::write(
        &p,
        "provider \"registry.terraform.io/hashicorp/google\" {\n  version = \"6.10.0\"\n}\n",
    )
    .unwrap();
    assert_eq!(
        parse_lock_file_at(&p).unwrap(),
        vec![Provider {
            address: "registry.terraform.io/hashicorp/google".into(),
            version: "6.10.0".into()
        }]
    );
}
