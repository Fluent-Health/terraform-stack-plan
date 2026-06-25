package cache

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

type ProviderCache struct {
	Store       StorageBackend
	Registry    *RegistryClient
	CacheDir    string
	Version     string
	Parallelism int
}

func NewProviderCache(store StorageBackend, cacheDir, version string) *ProviderCache {
	if cacheDir == "" {
		cacheDir = os.Getenv("TF_PLUGIN_CACHE_DIR")
	}
	if cacheDir == "" {
		cacheDir = "/workspace/.tf-plugin-cache"
	}
	return &ProviderCache{
		Store:       store,
		Registry:    NewRegistryClient(),
		CacheDir:    cacheDir,
		Version:     version,
		Parallelism: 8,
	}
}

func (c *ProviderCache) Warm(ctx context.Context, stackPaths []string) error {
	// 1. Gather all providers from lock files
	var list []Provider
	for _, stack := range stackPaths {
		lockPath := filepath.Join(stack, ".terraform.lock.hcl")
		if _, err := os.Stat(lockPath); err != nil {
			continue
		}
		ps, err := ParseLockFile(lockPath)
		if err != nil {
			continue
		}
		list = append(list, ps...)
	}

	// 2. De-duplicate providers
	unique := make(map[string]Provider)
	for _, p := range list {
		unique[p.Address+"/"+p.Version] = p
	}

	if len(unique) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, c.Parallelism)
	var errCount int32

	for _, p := range unique {
		wg.Add(1)
		go func(p Provider) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := c.restoreOne(ctx, p); err != nil {
				fmt.Fprintf(os.Stderr, "cache: restore %s %s failed: %v\n", p.Address, p.Version, err)
				atomic.AddInt32(&errCount, 1)
			}
		}(p)
	}
	wg.Wait()

	if errCount > 0 {
		return fmt.Errorf("failed to restore %d provider(s)", errCount)
	}
	return nil
}

func (c *ProviderCache) restoreOne(ctx context.Context, p Provider) error {
	dest := filepath.Join(c.CacheDir, p.Address, p.Version, "linux_amd64")
	binName := "terraform-provider-" + filepath.Base(p.Address)
	if _, err := os.Stat(filepath.Join(dest, binName)); err == nil {
		return nil // Already warm in local cache
	}

	key := fmt.Sprintf("%s/%s/linux_amd64.tar.gz", p.Address, p.Version)
	if c.Version != "" && c.Version != "0" {
		key = c.Version + "/" + key
	}

	exists, err := c.Store.Exists(ctx, key)
	if err != nil {
		return err
	}

	// Create CacheDir if not exists to hold temp files
	if err := os.MkdirAll(c.CacheDir, 0755); err != nil {
		return err
	}

	// Atomic write isolation: write to unique temp folder inside CacheDir
	tmpDir, err := os.MkdirTemp(c.CacheDir, "tmp_cache_*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if exists {
		// Restore from StorageBackend
		rc, _, err := c.Store.Get(ctx, key)
		if err != nil {
			return err
		}
		defer rc.Close()

		if err := extractTarGz(rc, tmpDir); err != nil {
			return err
		}
	} else {
		// Cache miss -> Fetch from Registry
		rc, _, err := c.Registry.Fetch(ctx, p)
		if err != nil {
			return err
		}
		defer rc.Close()

		if err := extractZip(rc, tmpDir); err != nil {
			return err
		}
	}

	// Move atomically (POSIX-atomic Rename)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.Rename(tmpDir, dest)
}

func extractTarGz(r io.Reader, dest string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, hdr.Name)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func extractZip(r io.Reader, dest string) error {
	// Stream zip into a temp file first since zip reader requires ReaderAt
	tmpZip, err := os.CreateTemp("", "provider_zip_*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpZip.Name())
	defer tmpZip.Close()

	size, err := io.Copy(tmpZip, r)
	if err != nil {
		return err
	}

	zr, err := zip.NewReader(tmpZip, size)
	if err != nil {
		return err
	}

	for _, file := range zr.File {
		path := filepath.Join(dest, file.Name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, file.Mode())
		if err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(f, rc)
		rc.Close()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *ProviderCache) Save(ctx context.Context) error {
	if _, err := os.Stat(c.CacheDir); os.IsNotExist(err) {
		return nil
	}

	var providerDirs []string
	// Walk CacheDir to find .../<addr>/<version>/linux_amd64 directories
	err := filepath.Walk(c.CacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && filepath.Base(path) == "linux_amd64" {
			// Check if it contains terraform-provider-*
			files, err := os.ReadDir(path)
			if err != nil {
				return nil
			}
			for _, f := range files {
				if !f.IsDir() && strings.HasPrefix(f.Name(), "terraform-provider-") {
					providerDirs = append(providerDirs, path)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	for _, dir := range providerDirs {
		// Extract address and version from path relative to CacheDir
		rel, err := filepath.Rel(c.CacheDir, dir)
		if err != nil {
			continue
		}
		// rel is <namespace_domain>/<namespace>/<name>/<version>/linux_amd64
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 4 {
			continue
		}
		version := parts[len(parts)-2]
		address := strings.Join(parts[:len(parts)-2], "/")

		key := fmt.Sprintf("%s/%s/linux_amd64.tar.gz", address, version)
		if c.Version != "" && c.Version != "0" {
			key = c.Version + "/" + key
		}

		exists, err := c.Store.Exists(ctx, key)
		if err != nil || exists {
			continue // Already saved or error occurred
		}

		// Create in-memory tar.gz
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)

		files, err := os.ReadDir(dir)
		if err != nil {
			gw.Close()
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			filePath := filepath.Join(dir, f.Name())
			fi, err := os.Stat(filePath)
			if err != nil {
				continue
			}
			hdr, err := tar.FileInfoHeader(fi, "")
			if err != nil {
				continue
			}
			hdr.Name = f.Name()
			if err := tw.WriteHeader(hdr); err != nil {
				continue
			}
			fileBytes, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			tw.Write(fileBytes)
		}

		tw.Close()
		gw.Close()

		if err := c.Store.Put(ctx, key, &buf, int64(buf.Len())); err != nil {
			fmt.Fprintf(os.Stderr, "cache: save %s %s failed: %v\n", address, version, err)
		} else {
			fmt.Printf("plugin-cache: saved %s\n", key)
		}
	}
	return nil
}
