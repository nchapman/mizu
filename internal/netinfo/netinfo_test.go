package netinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicIPCache_FetchesAndCaches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("203.0.113.5\n"))
	}))
	defer srv.Close()
	c := NewPublicIPCache()
	c.SetProviderForTest(srv.URL, srv.Client())

	ip, err := c.Get(context.Background())
	if err != nil || ip != "203.0.113.5" {
		t.Fatalf("ip=%q err=%v", ip, err)
	}
	// Second call is cached.
	ip2, _ := c.Get(context.Background())
	if ip2 != ip || calls != 1 {
		t.Errorf("not cached: calls=%d ip2=%q", calls, ip2)
	}
}

func TestPublicIPCache_RejectsNonIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-an-ip"))
	}))
	defer srv.Close()
	c := NewPublicIPCache()
	c.SetProviderForTest(srv.URL, srv.Client())
	if _, err := c.Get(context.Background()); err == nil {
		t.Fatal("expected error for non-IP body")
	}
}

func TestLookupDomain_NoDomainHintsOperator(t *testing.T) {
	res := LookupDomain(context.Background(), "", "1.2.3.4")
	if res.Matches {
		t.Error("Matches=true with empty domain")
	}
	if len(res.Hints) == 0 {
		t.Error("expected hint for empty domain")
	}
}

func TestLookupDomain_MissingPublicIPHints(t *testing.T) {
	res := LookupDomain(context.Background(), "example.com", "")
	if res.Matches {
		t.Error("Matches=true with no public IP")
	}
	found := false
	for _, h := range res.Hints {
		if strings.Contains(h, "Could not detect") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hints=%v", res.Hints)
	}
}
