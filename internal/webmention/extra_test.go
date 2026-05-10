package webmention

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// --- Store: Pending and ForTarget filtering ---

func TestStore_PendingOnlyReturnsPending(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	must := func(m Mention) {
		if err := st.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	must(Mention{Source: "s1", Target: "t", Status: StatusPending, ReceivedAt: now})
	must(Mention{Source: "s2", Target: "t", Status: StatusVerified, ReceivedAt: now, VerifiedAt: now})
	must(Mention{Source: "s3", Target: "t", Status: StatusRejected, ReceivedAt: now})

	pending, err := st.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Source != "s1" {
		t.Errorf("Pending = %+v, want only s1", pending)
	}

	verified, err := st.ForTarget(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0].Source != "s2" {
		t.Errorf("ForTarget = %+v, want only s2 (verified)", verified)
	}
	if verified[0].VerifiedAt.IsZero() {
		t.Errorf("VerifiedAt not populated")
	}
}

func TestStore_UpsertReplacesStatus(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.Upsert(ctx, Mention{Source: "s", Target: "t", Status: StatusPending, ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(ctx, Mention{Source: "s", Target: "t", Status: StatusVerified, ReceivedAt: now, VerifiedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ForTarget(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1 (UNIQUE source,target)", len(got))
	}
	if got[0].Status != StatusVerified {
		t.Errorf("Status=%q, want verified", got[0].Status)
	}
}

func TestStore_RecentVerifiedAcrossTargets(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(m Mention) {
		if err := st.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// Two verified mentions across different targets, plus pending and
	// rejected rows that must be excluded.
	must(Mention{Source: "https://a/p", Target: "https://me/x", Status: StatusVerified, ReceivedAt: now.Add(-2 * time.Hour), VerifiedAt: now.Add(-2 * time.Hour)})
	must(Mention{Source: "https://b/p", Target: "https://me/y", Status: StatusVerified, ReceivedAt: now.Add(-1 * time.Hour), VerifiedAt: now.Add(-1 * time.Hour)})
	must(Mention{Source: "https://c/p", Target: "https://me/z", Status: StatusPending, ReceivedAt: now})
	must(Mention{Source: "https://d/p", Target: "https://me/q", Status: StatusRejected, ReceivedAt: now})

	got, err := st.Recent(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2 verified", len(got))
	}
	if got[0].Source != "https://b/p" {
		t.Errorf("got[0].Source=%q, want newest first", got[0].Source)
	}
}

func TestStore_RecentRespectsLimit(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := st.Upsert(ctx, Mention{
			Source: fmt.Sprintf("https://a/%d", i), Target: "https://me/x",
			Status: StatusVerified, ReceivedAt: now.Add(time.Duration(i) * time.Second),
			VerifiedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Recent(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d, want 3", len(got))
	}
}

// --- RunVerifier startup re-enqueue ---

func TestRunVerifier_ReenqueuesPendingOnStartup(t *testing.T) {
	target := "https://example.com/post"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><a href="` + target + `">x</a></html>`))
	}))
	defer srv.Close()

	store := newStore(t)

	// Pre-seed a pending row as if a previous process shut down before draining.
	if err := store.Upsert(context.Background(), Mention{
		Source: srv.URL + "/source", Target: target,
		Status: StatusPending, ReceivedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	s := New(store, "https://example.com")
	s.http = http.DefaultClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunVerifier(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ms, err := s.ForTarget(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) == 1 && ms[0].Status == StatusVerified {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pending row never reached verified state")
}

// --- Send / SendForPost ---

func TestSend_PostsForm(t *testing.T) {
	var got struct {
		source, target, ctype string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.source = r.PostFormValue("source")
		got.target = r.PostFormValue("target")
		got.ctype = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := newService(t, "https://example.com")
	if err := s.Send(context.Background(), srv.URL, "https://example.com/p", "https://other/x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.source != "https://example.com/p" || got.target != "https://other/x" {
		t.Errorf("form values = %+v", got)
	}
	if got.ctype != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type=%q", got.ctype)
	}
}

func TestSend_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := newService(t, "https://example.com")
	err := s.Send(context.Background(), srv.URL, "https://a", "https://b")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSendForPost_DiscoversAndSendsExternal(t *testing.T) {
	var sendHits int32

	// External target: advertises a webmention endpoint; receiver records hits.
	var externalURL string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sendHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()

	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+receiver.URL+`>; rel="webmention"`)
		w.Write([]byte(`<html></html>`))
	}))
	defer external.Close()
	externalURL = external.URL + "/post"

	s := newService(t, "https://example.com")

	rendered := `<p>read <a href="` + externalURL + `">this</a> and ` +
		// Same-origin link should be skipped:
		`<a href="https://example.com/self">self</a></p>`
	s.SendForPost(context.Background(), "https://example.com/source", rendered)

	if got := atomic.LoadInt32(&sendHits); got != 1 {
		t.Errorf("receiver hits=%d, want 1 (same-origin link should be skipped)", got)
	}
}

// --- Receive validation paths ---

func TestReceive_RejectsEmpty(t *testing.T) {
	s := newService(t, "https://example.com")
	if err := s.Receive(context.Background(), "", "https://example.com/x"); err == nil {
		t.Error("expected error for empty source")
	}
	if err := s.Receive(context.Background(), "https://other/x", ""); err == nil {
		t.Error("expected error for empty target")
	}
}

func TestReceive_RejectsNonHTTPScheme(t *testing.T) {
	s := newService(t, "https://example.com")
	if err := s.Receive(context.Background(), "ftp://x/y", "https://example.com/p"); err == nil {
		t.Error("expected error for ftp source")
	}
	if err := s.Receive(context.Background(), "https://x/y", "javascript:alert(1)"); err == nil {
		t.Error("expected error for javascript: target")
	}
}
