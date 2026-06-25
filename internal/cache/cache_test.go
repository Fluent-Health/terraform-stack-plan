package cache

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type mockStorage struct {
	existsFunc func(ctx context.Context, key string) (bool, error)
	getFunc    func(ctx context.Context, key string) (io.ReadCloser, int64, error)
	putFunc    func(ctx context.Context, key string, r io.Reader, size int64) error
}

func (m *mockStorage) Exists(ctx context.Context, key string) (bool, error) {
	if m.existsFunc != nil {
		return m.existsFunc(ctx, key)
	}
	return false, nil
}

func (m *mockStorage) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, key)
	}
	return nil, 0, fmt.Errorf("get not implemented")
}

func (m *mockStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if m.putFunc != nil {
		return m.putFunc(ctx, key, r, size)
	}
	return nil
}

func TestProviderCacheWarming(t *testing.T) {
	tmpCacheDir := t.TempDir()
	stackDir := t.TempDir()

	// Create a dummy .terraform.lock.hcl file in the stackDir
	lockContent := []byte(`
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
`)
	if err := os.WriteFile(filepath.Join(stackDir, ".terraform.lock.hcl"), lockContent, 0644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Mock GCS server / StorageBackend:
	// Let's say google provider exists in storage, but null provider does not (will fetch from registry).
	googleKey := fmt.Sprintf("v1/registry.terraform.io/hashicorp/google/6.10.0/%s.tar.gz", platform)
	nullKey := fmt.Sprintf("v1/registry.terraform.io/hashicorp/null/3.2.1/%s.tar.gz", platform)

	storageGetCalled := false
	store := &mockStorage{
		existsFunc: func(ctx context.Context, key string) (bool, error) {
			if key == googleKey {
				return true, nil
			}
			if key == nullKey {
				return false, nil
			}
			return false, nil
		},
		getFunc: func(ctx context.Context, key string) (io.ReadCloser, int64, error) {
			if key == googleKey {
				storageGetCalled = true
				// Return a valid tar.gz containing terraform-provider-google
				pr, pw := io.Pipe()
				go func() {
					gw := gzip.NewWriter(pw)
					tw := tar.NewWriter(gw)
					hdr := &tar.Header{
						Name: "terraform-provider-google",
						Mode: 0755,
						Size: int64(len("google-binary-data")),
					}
					tw.WriteHeader(hdr)
					tw.Write([]byte("google-binary-data"))
					tw.Close()
					gw.Close()
					pw.Close()
				}()
				return pr, -1, nil
			}
			return nil, 0, fmt.Errorf("unexpected get key: %s", key)
		},
	}

	// Mock Registry Server
	registryZipCalled := false
	zipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryZipCalled = true
		// Let's write a zip with a mock binary for "null" provider
		w.Header().Set("Content-Type", "application/zip")
		// We'll create a zip in memory using archive/zip but let's do it via zip.Writer
		// For simplicity, we can do it in the handler
		importZip(w, "terraform-provider-null", "null-binary-data")
	}))
	defer zipSrv.Close()

	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock download endpoint returning zipSrv URL
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"download_url": "%s"}`, zipSrv.URL)
	}))
	defer regSrv.Close()

	pc := NewProviderCache(store, tmpCacheDir, "v1")
	pc.Registry = &RegistryClient{
		host:    "registry.terraform.io",
		baseURL: regSrv.URL,
		hc:      &http.Client{},
	}

	err := pc.Warm(context.Background(), []string{stackDir})
	if err != nil {
		t.Fatalf("Warm failed: %v", err)
	}

	if !storageGetCalled {
		t.Error("expected Google provider to be restored from storage, but Get was not called")
	}
	if !registryZipCalled {
		t.Error("expected Null provider to be fetched from registry, but registry zip was not called")
	}

	// Verify both providers exist in the cache directory
	googleBin := filepath.Join(tmpCacheDir, "registry.terraform.io/hashicorp/google/6.10.0", platform, "terraform-provider-google")
	if _, err := os.Stat(googleBin); err != nil {
		t.Errorf("google binary not found in cache: %v", err)
	} else {
		data, _ := os.ReadFile(googleBin)
		if string(data) != "google-binary-data" {
			t.Errorf("google binary data mismatch: %q", string(data))
		}
	}

	nullBin := filepath.Join(tmpCacheDir, "registry.terraform.io/hashicorp/null/3.2.1", platform, "terraform-provider-null")
	if _, err := os.Stat(nullBin); err != nil {
		t.Errorf("null binary not found in cache: %v", err)
	} else {
		data, _ := os.ReadFile(nullBin)
		if string(data) != "null-binary-data" {
			t.Errorf("null binary data mismatch: %q", string(data))
		}
	}
}

func TestProviderCacheSave(t *testing.T) {
	tmpCacheDir := t.TempDir()

	// Create a local provider manually in the cache directory
	localProviderDir := filepath.Join(tmpCacheDir, "registry.terraform.io/hashicorp/aws/5.0.0", platform)
	if err := os.MkdirAll(localProviderDir, 0755); err != nil {
		t.Fatalf("failed to create local provider dir: %v", err)
	}
	localBinPath := filepath.Join(localProviderDir, "terraform-provider-aws")
	if err := os.WriteFile(localBinPath, []byte("aws-binary-data"), 0755); err != nil {
		t.Fatalf("failed to write local bin: %v", err)
	}

	awsKey := fmt.Sprintf("v1/registry.terraform.io/hashicorp/aws/5.0.0/%s.tar.gz", platform)

	putCalled := false
	var putBytes []byte
	store := &mockStorage{
		existsFunc: func(ctx context.Context, key string) (bool, error) {
			if key == awsKey {
				return false, nil // Not on GCS yet
			}
			return false, nil
		},
		putFunc: func(ctx context.Context, key string, r io.Reader, size int64) error {
			if key == awsKey {
				putCalled = true
				var err error
				putBytes, err = io.ReadAll(r)
				return err
			}
			return fmt.Errorf("unexpected put key: %s", key)
		},
	}

	pc := NewProviderCache(store, tmpCacheDir, "v1")
	err := pc.Save(context.Background())
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if !putCalled {
		t.Error("expected aws provider to be saved to storage, but Put was not called")
	}

	// Decompress putBytes and verify contents
	gr, err := gzip.NewReader(strings.NewReader(string(putBytes)))
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	tr := tar.NewReader(gr)
	foundAwsBin := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar reader next failed: %v", err)
		}
		if hdr.Name == "terraform-provider-aws" {
			foundAwsBin = true
			data, _ := io.ReadAll(tr)
			if string(data) != "aws-binary-data" {
				t.Errorf("tar aws bin content mismatch: %q", string(data))
			}
		}
	}
	if !foundAwsBin {
		t.Error("terraform-provider-aws not found in saved tarball")
	}
}

// TestProviderCacheWarmConcurrent verifies that concurrent Warm() calls for the
// same provider are safe. Each goroutine calls Warm independently (simulating
// parallel CI runs sharing one cache dir); the atomic os.Rename ensures
// last-writer-wins with no partial reads or corruption.
func TestProviderCacheWarmConcurrent(t *testing.T) {
	const numConcurrent = 8

	tmpCacheDir := t.TempDir()
	stackDir := t.TempDir()

	lockContent := []byte(`
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 6.0.0"
  hashes = [
    "h1:mockhash",
  ]
}
`)
	if err := os.WriteFile(filepath.Join(stackDir, ".terraform.lock.hcl"), lockContent, 0644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	googleKey := fmt.Sprintf("v1/registry.terraform.io/hashicorp/google/6.10.0/%s.tar.gz", platform)
	var getCount int32

	store := &mockStorage{
		existsFunc: func(ctx context.Context, key string) (bool, error) {
			return key == googleKey, nil
		},
		getFunc: func(ctx context.Context, key string) (io.ReadCloser, int64, error) {
			atomic.AddInt32(&getCount, 1)
			pr, pw := io.Pipe()
			go func() {
				gw := gzip.NewWriter(pw)
				tw := tar.NewWriter(gw)
				content := "google-binary-data"
				hdr := &tar.Header{
					Name: "terraform-provider-google",
					Mode: 0755,
					Size: int64(len(content)),
				}
				tw.WriteHeader(hdr)       //nolint:errcheck
				tw.Write([]byte(content)) //nolint:errcheck
				tw.Close()
				gw.Close()
				pw.Close()
			}()
			return pr, -1, nil
		},
	}

	var wg sync.WaitGroup
	var errCount int32
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc := NewProviderCache(store, tmpCacheDir, "v1")
			if err := pc.Warm(context.Background(), []string{stackDir}); err != nil {
				atomic.AddInt32(&errCount, 1)
				t.Errorf("concurrent Warm failed: %v", err)
			}
		}()
	}
	wg.Wait()

	if errCount > 0 {
		t.Fatalf("%d Warm() goroutines failed", errCount)
	}

	googleBin := filepath.Join(tmpCacheDir, "registry.terraform.io/hashicorp/google/6.10.0", platform, "terraform-provider-google")
	data, err := os.ReadFile(googleBin)
	if err != nil {
		t.Fatalf("google binary not in cache after concurrent Warm: %v", err)
	}
	if string(data) != "google-binary-data" {
		t.Errorf("google binary content wrong after concurrent Warm: %q", string(data))
	}

	if getCount == 0 {
		t.Error("expected at least one GCS Get call across concurrent Warm goroutines")
	}
	t.Logf("concurrent Warm: %d GCS Get calls across %d goroutines (last-writer-wins, no corruption)", getCount, numConcurrent)
}

// helper to write a mock zip file
func importZip(w io.Writer, filename, content string) {
	zw := zip.NewWriter(w)
	f, err := zw.Create(filename)
	if err != nil {
		return
	}
	f.Write([]byte(content))
	zw.Close()
}
