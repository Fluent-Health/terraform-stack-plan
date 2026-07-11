package gcppam

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testGrant = "projects/proj-1/locations/global/entitlements/iam/grants/g1"

func TestDecideGrant(t *testing.T) {
	var gotPath, gotAuth, gotQuota, gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotQuota = r.Header.Get("x-goog-user-project")
		b, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(b, &body)
		gotReason = body["reason"]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	if err := DecideGrant(context.Background(), srv.URL, "quota-proj", "user-token", testGrant, true, "lgtm"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/"+testGrant+":approve" {
		t.Errorf("path: %q", gotPath)
	}
	if gotAuth != "Bearer user-token" || gotQuota != "quota-proj" || gotReason != "lgtm" {
		t.Errorf("headers/body: auth=%q quota=%q reason=%q", gotAuth, gotQuota, gotReason)
	}

	if err := DecideGrant(context.Background(), srv.URL, "", "user-token", testGrant, false, "nope"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, ":deny") {
		t.Errorf("deny path: %q", gotPath)
	}
	if gotQuota != "" {
		t.Errorf("quota header should be absent when unset: %q", gotQuota)
	}
}

func TestDecideGrantSurfacesPAMErrorVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"message":"Permission denied on grant"}}`)
	}))
	defer srv.Close()
	err := DecideGrant(context.Background(), srv.URL, "", "t", testGrant, true, "")
	de, ok := err.(*DecideError)
	if !ok || de.Status != 403 || !strings.Contains(de.Body, "Permission denied on grant") {
		t.Fatalf("want verbatim DecideError 403, got %v", err)
	}
}

func TestValidateGrantName(t *testing.T) {
	if err := ValidateGrantName(testGrant); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "grants/g1", "projects/p/grants/g1", "https://evil/" + testGrant, testGrant + "/extra"} {
		if err := ValidateGrantName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
