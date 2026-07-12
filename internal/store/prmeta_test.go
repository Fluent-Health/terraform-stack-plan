package store

import "testing"

func TestGetPRMetaAbsent(t *testing.T) {
	db := newTestDB(t)
	_, ok, err := GetPRMeta(db, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRMeta: %v", err)
	}
	if ok {
		t.Fatalf("ok = true for absent PR meta; want false")
	}
}

func TestUpsertPRMetaAndGet(t *testing.T) {
	db := newTestDB(t)
	m := PRMeta{
		Repo:        "owner/repo",
		PR:          42,
		Title:       "Add widget",
		Body:        "Some body",
		AuthorLogin: "octocat",
		HeadRef:     "feature/widget",
		URL:         "https://github.com/owner/repo/pull/42",
		AutoMerge:   true,
	}
	if err := UpsertPRMeta(db, m); err != nil {
		t.Fatalf("UpsertPRMeta: %v", err)
	}
	got, ok, err := GetPRMeta(db, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRMeta: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false after upsert; want true")
	}
	if got.Repo != m.Repo || got.PR != m.PR || got.Title != m.Title || got.Body != m.Body ||
		got.AuthorLogin != m.AuthorLogin || got.HeadRef != m.HeadRef || got.URL != m.URL || got.AutoMerge != m.AutoMerge {
		t.Errorf("got = %+v; want %+v", got, m)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt is zero")
	}
}

func TestGetPRMetaByPRFoundWithoutRepo(t *testing.T) {
	db := newTestDB(t)
	m := PRMeta{
		Repo:        "owner/repo",
		PR:          999,
		Title:       "Add gadget",
		Body:        "Some body",
		AuthorLogin: "octocat",
		HeadRef:     "feature/gadget",
		URL:         "https://github.com/owner/repo/pull/999",
		AutoMerge:   true,
	}
	if err := UpsertPRMeta(db, m); err != nil {
		t.Fatalf("UpsertPRMeta: %v", err)
	}
	// Looked up by pr alone — no repo, no execution required. This is the
	// window right after PR open/sync: pr_meta exists, no execution yet.
	got, ok, err := GetPRMetaByPR(db, 999)
	if err != nil {
		t.Fatalf("GetPRMetaByPR: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false; want true")
	}
	if got.Repo != m.Repo || got.PR != m.PR || got.Title != m.Title || got.Body != m.Body ||
		got.AuthorLogin != m.AuthorLogin || got.HeadRef != m.HeadRef || got.URL != m.URL || got.AutoMerge != m.AutoMerge {
		t.Errorf("got = %+v; want %+v", got, m)
	}
}

func TestGetPRMetaByPRAbsent(t *testing.T) {
	db := newTestDB(t)
	_, ok, err := GetPRMetaByPR(db, 42)
	if err != nil {
		t.Fatalf("GetPRMetaByPR: %v", err)
	}
	if ok {
		t.Fatalf("ok = true for absent PR meta; want false")
	}
}

func TestUpsertPRMetaUpdatesInPlace(t *testing.T) {
	db := newTestDB(t)
	m := PRMeta{Repo: "owner/repo", PR: 42, Title: "Original title", AuthorLogin: "octocat"}
	if err := UpsertPRMeta(db, m); err != nil {
		t.Fatalf("UpsertPRMeta: %v", err)
	}
	m.Title = "Changed title"
	if err := UpsertPRMeta(db, m); err != nil {
		t.Fatalf("UpsertPRMeta (re-upsert): %v", err)
	}
	got, ok, err := GetPRMeta(db, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRMeta: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false after re-upsert; want true")
	}
	if got.Title != "Changed title" {
		t.Errorf("Title = %q; want %q", got.Title, "Changed title")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pr_meta WHERE repo = ? AND pr = ?`, "owner/repo", 42).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d; want 1 (upsert must not duplicate)", count)
	}
}
