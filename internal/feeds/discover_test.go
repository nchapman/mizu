package feeds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscover_FindsRSSLinkInHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<html><head>
<link rel="alternate" type="application/rss+xml" href="/rss">
</head><body>hi</body></html>`))
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != srv.URL+"/rss" {
		t.Errorf("got %q, want %s/rss", got, srv.URL)
	}
}

func TestDiscover_PrefersRSSOverAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<html><head>
<link rel="alternate" type="application/atom+xml" href="/atom">
<link rel="alternate" type="application/rss+xml" href="/rss">
</head></html>`))
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.HasSuffix(got, "/rss") {
		t.Errorf("got %q, want RSS preferred", got)
	}
}

func TestDiscover_FallsBackToAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
<link rel="alternate" type="application/atom+xml" href="/atom.xml">
</head></html>`))
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.HasSuffix(got, "/atom.xml") {
		t.Errorf("got %q, want atom fallback", got)
	}
}

func TestDiscover_PassesThroughFeedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write([]byte(`<rss version="2.0"><channel></channel></rss>`))
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// httptest URLs end with the path "/" after redirect normalization.
	if !strings.HasPrefix(got, srv.URL) {
		t.Errorf("got %q, want passthrough of %q", got, srv.URL)
	}
}

func TestDiscover_SniffsFeedBodyWithGenericXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel/></rss>`))
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.HasPrefix(got, srv.URL) {
		t.Errorf("got %q, want body-sniff passthrough", got)
	}
}

func TestDiscover_ResolvesRelativeHref(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/blog/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
<link rel="alternate" type="application/rss+xml" href="feed.xml">
</head></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL+"/blog/")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != srv.URL+"/blog/feed.xml" {
		t.Errorf("got %q, want %s/blog/feed.xml", got, srv.URL)
	}
}

func TestDiscover_NotAFeedDespiteAtomNamespaceInProse(t *testing.T) {
	// An HTML page that mentions the Atom namespace URL in body text
	// must not be sniffed as a feed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><html><body>
The Atom namespace is http://www.w3.org/2005/Atom and the root
element is &lt;feed&gt;. Here's a docs page about it.
</body></html>`))
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if !errors.Is(err, ErrNoFeedFound) {
		t.Errorf("got %v, want ErrNoFeedFound (prose mention should not pass sniff)", err)
	}
}

func TestDiscover_4xxReturnsDiscoverFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if !errors.Is(err, ErrDiscoverFailed) {
		t.Errorf("got %v, want ErrDiscoverFailed for 404", err)
	}
}

func TestDiscover_UnreachableHostReturnsDiscoverFailed(t *testing.T) {
	// Start a server, capture its URL, close it: any subsequent dial to
	// that port refuses cleanly and the error must classify as
	// ErrDiscoverFailed (not bubble up as 500).
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := Discover(context.Background(), http.DefaultClient, url)
	if !errors.Is(err, ErrDiscoverFailed) {
		t.Errorf("got %v, want ErrDiscoverFailed for closed listener", err)
	}
}

func TestDiscover_ReturnsErrNoFeedFoundWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>nothing</title></head></html>`))
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != ErrNoFeedFound {
		t.Errorf("got %v, want ErrNoFeedFound", err)
	}
}

func TestNormalizeDiscoverInput(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"news.ycombinator.com", "https://news.ycombinator.com", false},
		{"http://x", "http://x", false},
		{"https://x", "https://x", false},
		{"  https://x  ", "https://x", false},
		{"ftp://x", "", true},
		{"javascript://evil", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeDiscoverInput(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestSubscribe_ResolvesViaDiscovery(t *testing.T) {
	// HTML page advertising a feed at /feed.xml. Subscribe should
	// follow autodiscovery and persist the feed URL, not the page URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
<link rel="alternate" type="application/rss+xml" href="/feed.xml">
</head></html>`))
	}))
	defer srv.Close()

	s := newService(t)
	// Restore real discovery (the test helper installed a passthrough).
	s.discover = Discover
	s.discoverHTTP = http.DefaultClient

	feed, err := s.Subscribe(context.Background(), srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if feed.URL != srv.URL+"/feed.xml" {
		t.Errorf("persisted URL %q, want %s/feed.xml", feed.URL, srv.URL)
	}
}

func TestDiscover_HonorsRelTokenList(t *testing.T) {
	// rel="me alternate" should still match (it's a token list).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
<link rel="me alternate" type="application/rss+xml" href="/r">
</head></html>`))
	}))
	defer srv.Close()
	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	if err != nil || !strings.HasSuffix(got, "/r") {
		t.Errorf("rel token list: got %q err=%v", got, err)
	}
}
