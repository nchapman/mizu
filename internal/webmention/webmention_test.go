package webmention

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newService(t *testing.T, baseURL string) *Service {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	logger, err := NewLogger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, logger, baseURL)
	// Tests inject their own HTTP client that doesn't go through the
	// SSRF filter, since httptest servers bind to 127.0.0.1.
	s.http = http.DefaultClient
	return s
}

// --- discovery ---

func TestParseLinkHeader(t *testing.T) {
	cases := []struct {
		raw, rel, want string
		ok             bool
	}{
		{`<https://example.com/wm>; rel="webmention"`, "webmention", "https://example.com/wm", true},
		{`<https://example.com/hub>; rel=hub, <https://example.com/wm>; rel="webmention"`, "webmention", "https://example.com/wm", true},
		{`<https://example.com/wm>; rel="hub webmention"`, "webmention", "https://example.com/wm", true},
		{`<https://example.com/wm>; rel="hub"`, "webmention", "", false},
		{`not a link header`, "webmention", "", false},
	}
	for _, c := range cases {
		got, ok := parseLinkHeader(c.raw, c.rel)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLinkHeader(%q) = (%q, %v), want (%q, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestDiscover_LinkHeader(t *testing.T) {
	// Send two Link headers to exercise multi-header handling. Use
	// Add (not Set) so the first isn't overwritten. parseLinkHeader
	// returns the first match; we expect the absolute URL to win.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", `<https://elsewhere.example/wm>; rel="webmention"`)
		w.Header().Add("Link", `</relative>; rel="other"`)
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()
	got, err := Discover(context.Background(), http.DefaultClient, srv.URL+"/post")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://elsewhere.example/wm" {
		t.Errorf("got %q, want %q", got, "https://elsewhere.example/wm")
	}
}

func TestDiscover_LinkHeaderRelative(t *testing.T) {
	// Relative endpoint URL should be resolved against the request URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", `</wm>; rel="webmention"`)
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()
	got, err := Discover(context.Background(), http.DefaultClient, srv.URL+"/post")
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/wm" {
		t.Errorf("got %q, want %q", got, srv.URL+"/wm")
	}
}

func TestDiscover_HTMLFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><link rel="webmention" href="/wm"></head></html>`))
	}))
	defer srv.Close()
	got, err := Discover(context.Background(), http.DefaultClient, srv.URL+"/post")
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/wm" {
		t.Errorf("got %q, want %q", got, srv.URL+"/wm")
	}
}

func TestDiscover_NoEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html></html>`))
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), http.DefaultClient, srv.URL+"/post")
	if err != ErrNoEndpoint {
		t.Errorf("got %v, want ErrNoEndpoint", err)
	}
}

// --- verify ---

func TestVerify_LinkPresent(t *testing.T) {
	target := "https://example.com/post"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>I read <a href="` + target + `">this</a></body></html>`))
	}))
	defer srv.Close()
	if err := Verify(context.Background(), http.DefaultClient, srv.URL, target); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerify_LinkAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>no link here</body></html>`))
	}))
	defer srv.Close()
	err := Verify(context.Background(), http.DefaultClient, srv.URL, "https://example.com/post")
	if err != ErrLinkNotFound {
		t.Errorf("got %v, want ErrLinkNotFound", err)
	}
}

func TestVerify_TrailingSlashTolerant(t *testing.T) {
	target := "https://example.com/post"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><a href="` + target + `/">x</a></html>`))
	}))
	defer srv.Close()
	if err := Verify(context.Background(), http.DefaultClient, srv.URL, target); err != nil {
		t.Errorf("Verify (trailing slash): %v", err)
	}
}

func TestVerify_GoneRemovesMention(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	err := Verify(context.Background(), http.DefaultClient, srv.URL, "https://example.com/post")
	if err != ErrLinkNotFound {
		t.Errorf("got %v, want ErrLinkNotFound", err)
	}
}

// --- Receive + verifier integration ---

func TestService_ReceiveAndVerify_HappyPath(t *testing.T) {
	target := "https://example.com/post"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><a href="` + target + `">link</a></html>`))
	}))
	defer srv.Close()

	s := newService(t, "https://example.com")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunVerifier(ctx)

	if err := s.Receive(ctx, srv.URL+"/source", target); err != nil {
		t.Fatal(err)
	}

	// Poll the store; verification is async.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mentions, err := s.ForTarget(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if len(mentions) == 1 && mentions[0].Status == StatusVerified {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("mention never reached verified state")
}

func TestService_Receive_RejectsOffsiteTarget(t *testing.T) {
	s := newService(t, "https://example.com")
	err := s.Receive(context.Background(), "https://other.example/post", "https://elsewhere.com/post")
	if err != ErrBadTarget {
		t.Errorf("got %v, want ErrBadTarget", err)
	}
}

func TestService_Receive_RejectsSameSource(t *testing.T) {
	s := newService(t, "https://example.com")
	err := s.Receive(context.Background(), "https://example.com/post", "https://example.com/post")
	if err != ErrSameSource {
		t.Errorf("got %v, want ErrSameSource", err)
	}
}

func TestService_TransientErrorLeavesPending(t *testing.T) {
	// 500 responses mean we couldn't determine link presence. The
	// row must stay at StatusPending so a later restart re-tries.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := newService(t, "https://example.com")
	target := "https://example.com/post"
	if err := s.Receive(context.Background(), srv.URL+"/source", target); err != nil {
		t.Fatal(err)
	}
	// Process exactly one job synchronously instead of running the loop.
	j := <-s.queue
	s.processOne(context.Background(), j)

	pending, err := s.store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending rows, want 1 (transient errors should not flip to rejected)", len(pending))
	}
}

func TestService_TargetTrailingDotNormalized(t *testing.T) {
	s := newService(t, "https://example.com")
	// A target with a trailing dot on the host should be accepted.
	err := s.Receive(context.Background(), "https://other.example/x", "https://example.com./post")
	if err == ErrBadTarget {
		t.Errorf("trailing-dot target rejected; should be accepted")
	}
	// Verifier will fail (no real source), but Receive itself should not.
}

// --- link extraction ---

func TestExtractLinks_DedupesAndFiltersScheme(t *testing.T) {
	html := `<p>see <a href="https://a.example/x">a</a> and
		<a href="https://b.example/y">b</a> and
		<a href="https://a.example/x">a again</a> and
		<a href="mailto:foo@bar">mail</a></p>`
	got := extractLinks(html)
	want := []string{"https://a.example/x", "https://b.example/y"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}
