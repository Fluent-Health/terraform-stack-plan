package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
)

// gcsObjectStore implements server.ObjectStore over the GCS JSON API (dependency-
// free; injected token, like the gcs lock). Objects live at <prefix>/<key>.
type gcsObjectStore struct {
	token   func(context.Context) (string, error)
	bucket  string
	prefix  string
	baseURL string
	hc      *http.Client
}

func newGCSObjectStore(token func(context.Context) (string, error), bucket, prefix, baseURL string) *gcsObjectStore {
	if baseURL == "" {
		baseURL = "https://storage.googleapis.com"
	}
	return &gcsObjectStore{token: token, bucket: bucket, prefix: prefix, baseURL: strings.TrimRight(baseURL, "/"), hc: &http.Client{Timeout: 60 * time.Second}}
}

func (s *gcsObjectStore) name(key string) string {
	if s.prefix == "" {
		return key
	}
	return strings.TrimSuffix(s.prefix, "/") + "/" + key
}

func (s *gcsObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	tok, err := s.token(ctx)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		s.baseURL, url.PathEscape(s.bucket), url.QueryEscape(s.name(key)))

	// Stream the body. The offload path passes an *os.File, so set Content-Length
	// from its size and hand the file to the request directly — net/http streams
	// it without buffering the whole log into memory. Any other reader (size
	// unknown) falls back to reading it in.
	var body io.Reader
	var clen int64 = -1
	if f, ok := r.(*os.File); ok {
		if fi, sterr := f.Stat(); sterr == nil {
			body, clen = f, fi.Size()
		}
	}
	if body == nil {
		data, rerr := io.ReadAll(r)
		if rerr != nil {
			return rerr
		}
		body, clen = bytes.NewReader(data), int64(len(data))
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if clen >= 0 {
		req.ContentLength = clen
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gcs put %s: %d: %s", s.name(key), resp.StatusCode, b)
	}
	return nil
}

func (s *gcsObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	tok, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		s.baseURL, url.PathEscape(s.bucket), url.QueryEscape(s.name(key)))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("gcs get %s: %d", s.name(key), resp.StatusCode)
	}
	return resp.Body, nil
}

var _ server.ObjectStore = (*gcsObjectStore)(nil)
