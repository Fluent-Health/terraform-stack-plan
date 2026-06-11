package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

var _ statemove.Locker = (*gcsLocker)(nil)

// gcsBackend scans the *.tf files in stackDir for a
// `terraform { backend "gcs" { bucket = ...; prefix = ... } }` block and returns
// its bucket and prefix. ok is false if no gcs backend is configured.
func gcsBackend(stackDir string) (bucket, prefix string, ok bool) {
	entries, err := os.ReadDir(stackDir)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tf" {
			continue
		}
		name := filepath.Join(stackDir, e.Name())
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		f, diags := hclsyntax.ParseConfig(data, e.Name(), hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			continue
		}
		body, bok := f.Body.(*hclsyntax.Body)
		if !bok {
			continue
		}
		for _, blk := range body.Blocks {
			if blk.Type != "terraform" {
				continue
			}
			for _, inner := range blk.Body.Blocks {
				if inner.Type != "backend" || len(inner.Labels) != 1 || inner.Labels[0] != "gcs" {
					continue
				}
				b, _ := attrString(inner.Body, "bucket")
				p, _ := attrString(inner.Body, "prefix")
				if b != "" {
					return b, p, true
				}
			}
		}
	}
	return "", "", false
}

// attrString reads a literal string attribute from an hclsyntax body.
func attrString(body *hclsyntax.Body, name string) (string, bool) {
	attr, ok := body.Attributes[name]
	if !ok {
		return "", false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().IsPrimitiveType() {
		return "", false
	}
	return v.AsString(), true
}

// gcsLocker acquires terraform's GCS-backend `.tflock` object (an
// ifGenerationMatch=0 upload) as a pessimistic, fail-fast lock.
type gcsLocker struct {
	token   func(context.Context) (string, error)
	baseURL string
	hc      *http.Client
}

// newGCSLocker builds a gcsLocker. An empty baseURL defaults to the real GCS
// JSON API endpoint.
func newGCSLocker(token func(context.Context) (string, error), baseURL string) *gcsLocker {
	if baseURL == "" {
		baseURL = "https://storage.googleapis.com"
	}
	return &gcsLocker{token: token, baseURL: baseURL, hc: http.DefaultClient}
}

// lockInfo mirrors terraform's GCS backend lock object payload.
type lockInfo struct {
	ID        string `json:"ID"`
	Operation string `json:"Operation"`
	Who       string `json:"Who"`
	Version   string `json:"Version"`
	Created   string `json:"Created"`
	Path      string `json:"Path"`
}

// randID returns a hex-encoded random identifier.
func randID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (l *gcsLocker) Acquire(ctx context.Context, stackDir string) (func() error, error) {
	bucket, prefix, ok := gcsBackend(stackDir)
	if !ok {
		return nil, fmt.Errorf("no gcs backend configured in %s", stackDir)
	}
	object := prefix + "/default.tflock"

	who := "tfstackplan"
	if u, err := user.Current(); err == nil && u.Username != "" {
		who = u.Username
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		who += "@" + h
	}
	info := lockInfo{
		ID:        randID(),
		Operation: "tfstackplan state apply",
		Who:       who,
		Version:   "tfstackplan",
		Created:   time.Now().UTC().Format(time.RFC3339Nano),
		Path:      fmt.Sprintf("gs://%s/%s", bucket, object),
	}
	body, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal lock info: %w", err)
	}

	token, err := l.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth token: %w", err)
	}

	uploadURL := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s&ifGenerationMatch=0",
		l.baseURL, bucket, url.QueryEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, fmt.Errorf("state %s is already locked (gs://%s/%s)", stackDir, bucket, object)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("acquire lock: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}

	release := func() error {
		token, err := l.token(context.Background())
		if err != nil {
			return fmt.Errorf("auth token: %w", err)
		}
		delURL := fmt.Sprintf("%s/storage/v1/b/%s/o/%s", l.baseURL, bucket, url.QueryEscape(object))
		req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, delURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := l.hc.Do(req)
		if err != nil {
			return fmt.Errorf("release lock: %w", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return release, nil
}
