package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

type RegistryClient struct {
	// host is the registry hostname this client is configured for (e.g.
	// "registry.terraform.io"). Addresses whose hostname matches are routed to
	// baseURL; addresses with a different hostname use their own HTTPS URL.
	host    string
	baseURL string
	hc      *http.Client
}

func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		host:    "registry.terraform.io",
		baseURL: "https://registry.terraform.io",
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

var defaultRegistry = NewRegistryClient()

func FetchFromRegistry(ctx context.Context, p Provider) (io.ReadCloser, int64, error) {
	return defaultRegistry.Fetch(ctx, p)
}

func (r *RegistryClient) Fetch(ctx context.Context, p Provider) (io.ReadCloser, int64, error) {
	downloadURL, err := r.GetDownloadURL(ctx, p)
	if err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}

	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("registry fetch download status: %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

func (r *RegistryClient) GetDownloadURL(ctx context.Context, p Provider) (string, error) {
	parts := strings.Split(p.Address, "/")
	var ns, name, apiBase string
	if len(parts) >= 3 {
		ns, name = parts[len(parts)-2], parts[len(parts)-1]
		// Route to the registry embedded in the address unless it matches the
		// configured host (which may point to a test server via baseURL).
		if parts[0] == r.host {
			apiBase = r.baseURL
		} else {
			apiBase = "https://" + parts[0]
		}
	} else if len(parts) == 2 {
		ns, name = parts[0], parts[1]
		apiBase = r.baseURL
	} else {
		return "", fmt.Errorf("invalid provider address: %s", p.Address)
	}

	u := fmt.Sprintf("%s/v1/providers/%s/%s/%s/download/%s/%s", apiBase, ns, name, p.Version, runtime.GOOS, runtime.GOARCH)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}

	resp, err := r.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("registry status: %d", resp.StatusCode)
	}

	var payload struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.DownloadURL, nil
}
