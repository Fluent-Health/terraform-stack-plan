package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func pushBody(t *testing.T, data map[string]string) string {
	t.Helper()
	var raw []byte
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	env := map[string]any{"message": map[string]any{"data": base64.StdEncoding.EncodeToString(raw)}, "subscription": "s"}
	b, _ := json.Marshal(env)
	return string(b)
}

func TestPushEventVerifies(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{PushServiceAccount: "pusher@x.iam.gserviceaccount.com"})
	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Stacks: []events.StackState{{Path: "s/a"}}})
	a.PushVerifier = func(_ context.Context, bearer string) (string, error) {
		if bearer == "good" {
			return "pusher@x.iam.gserviceaccount.com", nil
		}
		return "", fmt.Errorf("bad token")
	}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	do := func(auth, body string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/pubsub/push", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if c := do("good", pushBody(t, map[string]string{"id": "e1"})); c != 204 {
		t.Errorf("valid push = %d, want 204", c)
	}
	if c := do("", pushBody(t, nil)); c != 401 {
		t.Errorf("missing token = %d, want 401", c)
	}
	if c := do("bad", pushBody(t, nil)); c != 401 {
		t.Errorf("bad token = %d, want 401", c)
	}
	if c := do("good", "not json"); c != 400 {
		t.Errorf("bad envelope = %d, want 400", c)
	}
}

func TestPushEventWrongEmail(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{PushServiceAccount: "allowed@x"})
	a.PushVerifier = func(context.Context, string) (string, error) { return "intruder@y", nil }
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/pubsub/push", strings.NewReader(pushBody(t, nil)))
	req.Header.Set("Authorization", "Bearer x")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("wrong email = %d, want 403", resp.StatusCode)
	}
}

func TestPushEventDisabledWithoutVerifier(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{}) // no PushVerifier
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/pubsub/push", "application/json", strings.NewReader(pushBody(t, nil)))
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("disabled push = %d, want 404", resp.StatusCode)
	}
}
