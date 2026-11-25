package main

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/cache"
)

// An empty baseURL falls back to cache.GCSBaseURL (the $TFSTACKPLAN_GCS_URL
// override, else the real endpoint); an explicit one wins.
func TestGCSClientsDefaultToGCSBaseURL(t *testing.T) {
	old := cache.GCSBaseURL
	t.Cleanup(func() { cache.GCSBaseURL = old })
	cache.GCSBaseURL = "http://fake-gcs"

	if got := newGCSLocker(nil, "").baseURL; got != "http://fake-gcs" {
		t.Errorf("locker default baseURL = %q, want http://fake-gcs", got)
	}
	if got := newGCSLocker(nil, "http://explicit").baseURL; got != "http://explicit" {
		t.Errorf("locker explicit baseURL = %q, want http://explicit", got)
	}
	if got := newGCSObjectStore(nil, "b", "p", "").baseURL; got != "http://fake-gcs" {
		t.Errorf("object store default baseURL = %q, want http://fake-gcs", got)
	}
	if got := newGCSObjectStore(nil, "b", "p", "http://explicit/").baseURL; got != "http://explicit" {
		t.Errorf("object store explicit baseURL = %q, want http://explicit", got)
	}
}
