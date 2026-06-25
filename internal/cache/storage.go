package cache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type StorageBackend interface {
	Exists(ctx context.Context, key string) (bool, error)
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Put(ctx context.Context, key string, r io.Reader, size int64) error
}

type GCSStorage struct {
	bucket  string
	prefix  string
	baseURL string
	token   func(context.Context) (string, error)
	hc      *http.Client
}

func NewGCSStorage(token func(context.Context) (string, error), bucket, prefix string) *GCSStorage {
	return &GCSStorage{
		bucket:  bucket,
		prefix:  prefix,
		baseURL: "https://storage.googleapis.com",
		token:   token,
		hc:      &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GCSStorage) name(key string) string {
	if g.prefix == "" {
		return key
	}
	return strings.TrimSuffix(g.prefix, "/") + "/" + key
}

func (g *GCSStorage) Exists(ctx context.Context, key string) (bool, error) {
	tok, err := g.token(ctx)
	if err != nil {
		return false, err
	}
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
		g.baseURL, url.PathEscape(g.bucket), url.QueryEscape(g.name(key)))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("gcs status: %d", resp.StatusCode)
	}
	return true, nil
}

func (g *GCSStorage) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	tok, err := g.token(ctx)
	if err != nil {
		return nil, 0, err
	}
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		g.baseURL, url.PathEscape(g.bucket), url.QueryEscape(g.name(key)))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("gcs get status: %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

func (g *GCSStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	tok, err := g.token(ctx)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		g.baseURL, url.PathEscape(g.bucket), url.QueryEscape(g.name(key)))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, r)
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := g.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("gcs put status: %d", resp.StatusCode)
	}
	return nil
}
