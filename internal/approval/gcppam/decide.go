package gcppam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DecideGrant approves or denies one PAM grant AS THE GIVEN BEARER — a
// human's short-lived OAuth access token from the UI's incremental-consent
// popup, never a service identity. The server deliberately holds no approver
// capability of its own: GCP enforces the real authorization (a caller
// without PAM approver IAM gets a 403, surfaced verbatim via *DecideError),
// and the PAM audit log records the human.
//
// baseURL "" means the real PAM endpoint; tests point it at a fake.
// quotaProject, when non-empty, rides x-goog-user-project (user-credential
// API calls attribute quota to the OAuth client's project, which must have
// the PAM API enabled).
func DecideGrant(ctx context.Context, baseURL, quotaProject, bearer, grantName string, approve bool, reason string) error {
	if err := ValidateGrantName(grantName); err != nil {
		return err
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	verb := ":deny"
	if approve {
		verb = ":approve"
	}
	body, _ := json.Marshal(map[string]string{"reason": reason})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/"+grantName+verb, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if quotaProject != "" {
		req.Header.Set("x-goog-user-project", quotaProject)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("pam %s: %w", verb, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &DecideError{Status: resp.StatusCode, Body: string(b)}
	}
	return nil
}

// DecideError carries PAM's own rejection verbatim — the UI surfaces it
// as-is (e.g. the 403 for a user without approver IAM).
type DecideError struct {
	Status int
	Body   string
}

func (e *DecideError) Error() string {
	return fmt.Sprintf("pam: %d: %s", e.Status, e.Body)
}

// grantNameRE is the PAM grant resource shape: scope/target/locations/
// <loc>/entitlements/<ent>/grants/<id>. Validated before the call so a
// mangled name fails fast instead of producing a confusing PAM 404.
var grantNameRE = regexp.MustCompile(`^(projects|folders|organizations)/[^/]+/locations/[^/]+/entitlements/[^/]+/grants/[^/]+$`)

// ValidateGrantName rejects strings that are not a PAM grant resource name.
func ValidateGrantName(name string) error {
	if !grantNameRE.MatchString(name) {
		return fmt.Errorf("not a PAM grant resource name: %q", name)
	}
	return nil
}
