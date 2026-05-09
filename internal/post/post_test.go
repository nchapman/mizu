package post

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreate_FreezesSlug(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("Hello World", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "hello-world" {
		t.Errorf("Slug=%q want hello-world", p.Slug)
	}
	if !strings.HasSuffix(p.Path(), "/hello-world") {
		t.Errorf("Path=%q want suffix /hello-world", p.Path())
	}
}

func TestUpdate_TitleChangeKeepsSlug(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("Hello World", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := p.Path()
	originalFile := p.Filename

	updated, err := s.Update(p.ID, "Goodbye World", "new body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Goodbye World" {
		t.Errorf("Title=%q", updated.Title)
	}
	if updated.Path() != originalPath {
		t.Errorf("Path drifted: was %q, now %q", originalPath, updated.Path())
	}
	if updated.Filename != originalFile {
		t.Errorf("Filename drifted: was %q, now %q", originalFile, updated.Filename)
	}
	// Round-trip: reload from disk and verify the slug persisted.
	s2, err := NewStore(s.dir[:len(s.dir)-len("/posts")])
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.ByID(p.ID)
	if !ok {
		t.Fatal("post missing after reload")
	}
	if got.Slug != "hello-world" {
		t.Errorf("reloaded Slug=%q want hello-world", got.Slug)
	}
	if got.Title != "Goodbye World" {
		t.Errorf("reloaded Title=%q", got.Title)
	}
}

func TestUpdate_NoteBodyChange(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("", "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(p.ID, "", "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Body != "second" {
		t.Errorf("Body=%q", updated.Body)
	}
	if updated.Path() != p.Path() {
		t.Errorf("Path drifted")
	}
}

func TestUpdate_LegacyPostFreezesSlugFromCurrentTitle(t *testing.T) {
	// A post written before the Slug field existed has Slug=="" on
	// load. The first edit must capture a slug derived from the
	// PRE-edit title so the URL stays stable, not drift to a slug
	// derived from the new title.
	dir := t.TempDir()
	postsDir := filepath.Join(dir, "posts")
	if err := os.MkdirAll(postsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `---
id: legacy1
title: Old Title
date: 2024-01-15T10:00:00Z
---

original body
`
	if err := os.WriteFile(filepath.Join(postsDir, "2024-01-15-old-title-lega.md"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.Update("legacy1", "Brand New Title", "edited", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slug != "old-title" {
		t.Errorf("Slug=%q, want old-title (frozen from pre-edit title)", updated.Slug)
	}
	if !strings.HasSuffix(updated.Path(), "/old-title") {
		t.Errorf("Path=%q drifted; should still end /old-title", updated.Path())
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.Update("nope", "x", "y", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err=%v want ErrNotFound", err)
	}
}

func TestUpdate_RejectsTypeChange(t *testing.T) {
	s := newStore(t)
	// Note → article.
	p, err := s.Create("", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(p.ID, "Now Has Title", "body", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot toggle") {
		t.Errorf("err=%v want toggle rejection", err)
	}
}

func TestDelete_RemovesFileAndIndex(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, p.Filename)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be gone: err=%v", err)
	}
	if _, ok := s.ByID(p.ID); ok {
		t.Error("post still in index after delete")
	}
	if len(s.Recent(10)) != 0 {
		t.Error("Recent() still returns deleted post")
	}
}

func TestDelete_NotFound(t *testing.T) {
	s := newStore(t)
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err=%v want ErrNotFound", err)
	}
}

func TestDraft_CreateUpdateDelete(t *testing.T) {
	s := newStore(t)
	d, err := s.CreateDraft("Hello", "first body", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Title != "Hello" || d.Body != "first body" {
		t.Errorf("unexpected draft %+v", d)
	}
	if got := s.ListDrafts(); len(got) != 1 || got[0].ID != d.ID {
		t.Errorf("ListDrafts mismatch: %+v", got)
	}

	// Drafts must NOT appear in published listings.
	if posts := s.Recent(10); len(posts) != 0 {
		t.Errorf("Recent() leaked drafts: %d", len(posts))
	}

	if _, err := s.UpdateDraft(d.ID, "Hello (edited)", "second body", nil); err != nil {
		t.Fatal(err)
	}
	got, ok := s.DraftByID(d.ID)
	if !ok || got.Title != "Hello (edited)" || got.Body != "second body" {
		t.Errorf("UpdateDraft didn't take: %+v", got)
	}

	if err := s.DeleteDraft(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.DraftByID(d.ID); ok {
		t.Error("draft still in index after delete")
	}
}

func TestDraft_PublishMovesToPosts(t *testing.T) {
	s := newStore(t)
	d, err := s.CreateDraft("Hello World", "the body", nil)
	if err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(s.draftDir, d.Filename)

	p, err := s.Publish(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != d.ID {
		t.Errorf("published ID drifted: %q vs %q", p.ID, d.ID)
	}
	if p.Slug != "hello-world" {
		t.Errorf("Slug=%q", p.Slug)
	}
	if p.Date.IsZero() {
		t.Error("publish should set Date")
	}
	// Draft should be gone from disk and index.
	if _, err := os.Stat(draftPath); !os.IsNotExist(err) {
		t.Errorf("draft file still on disk: err=%v", err)
	}
	if _, ok := s.DraftByID(d.ID); ok {
		t.Error("draft still in index after publish")
	}
	// Post should be present.
	if got, ok := s.ByID(p.ID); !ok || got.Title != "Hello World" {
		t.Errorf("post missing or wrong: %+v ok=%v", got, ok)
	}
}

func TestDraft_PublishHonorsSlugCollision(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("Hello", "existing", nil); err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDraft("Hello", "draft body", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(d.ID)
	if !errors.Is(err, ErrSlugTaken) {
		t.Errorf("err=%v want ErrSlugTaken", err)
	}
	// Draft should still exist on collision so the user can retitle.
	if _, ok := s.DraftByID(d.ID); !ok {
		t.Error("failed publish should leave draft intact")
	}
}

func TestFrontmatter_PreservesIntentionalBlankLines(t *testing.T) {
	// One blank line after the closing `---` is the conventional
	// separator and gets consumed. Any extra blank lines belong to
	// the body and must survive a round-trip on disk.
	dir := t.TempDir()
	postsDir := filepath.Join(dir, "posts")
	if err := os.MkdirAll(postsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nid: blanks\ndate: 2024-01-01T00:00:00Z\n---\n\n\n\nstanza\n"
	if err := os.WriteFile(filepath.Join(postsDir, "blanks.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.ByID("blanks")
	if !ok {
		t.Fatal("post not found")
	}
	// One blank line is the conventional separator and is consumed;
	// the remaining blank lines (and the body) survive.
	if p.Body != "\n\nstanza\n" {
		t.Errorf("body=%q, want %q", p.Body, "\n\nstanza\n")
	}
}

func TestDraft_ReloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDraft("Round Trip", "body", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.DraftByID(d.ID)
	if !ok {
		t.Fatal("draft not found after reload")
	}
	if got.Title != "Round Trip" || strings.TrimSpace(got.Body) != "body" {
		t.Errorf("reloaded draft mismatch: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "a" {
		t.Errorf("tags not preserved: %v", got.Tags)
	}
}

func TestDelete_FreesSlugForRecreate(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("Hello", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	// Same title, same day — without proper slug cleanup this would
	// hit ErrSlugTaken.
	if _, err := s.Create("Hello", "body", nil); err != nil {
		t.Errorf("recreate failed: %v", err)
	}
}
