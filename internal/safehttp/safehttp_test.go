package safehttp

import (
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"rfc1918 10/8", "10.0.0.1", true},
		{"rfc1918 192.168", "192.168.1.1", true},
		{"rfc1918 172.16", "172.16.0.1", true},
		{"link-local v4", "169.254.169.254", true},
		{"link-local v6", "fe80::1", true},
		{"unique-local v6", "fc00::1", true},
		{"multicast v4", "224.0.0.1", true},
		{"multicast v6", "ff02::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"public v4", "8.8.8.8", false},
		{"public v6", "2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip := net.ParseIP(c.ip)
			if ip == nil {
				t.Fatalf("ParseIP %q returned nil", c.ip)
			}
			if got := IsBlockedIP(ip); got != c.want {
				t.Errorf("IsBlockedIP(%s) = %v, want %v", c.ip, got, c.want)
			}
		})
	}
}

func TestNewClient_Timeout(t *testing.T) {
	c := NewClient()
	if c.Timeout != defaultRequestTimeout {
		t.Errorf("Timeout=%v, want %v", c.Timeout, defaultRequestTimeout)
	}
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Errorf("Transport=%T, want *http.Transport", c.Transport)
	}
}

func TestNewClient_BlocksLoopbackByName(t *testing.T) {
	c := NewClient()
	// localhost resolves to a loopback IP, which IsBlockedIP rejects.
	_, err := c.Get("http://localhost/")
	if err == nil {
		t.Fatal("expected error fetching http://localhost/, got nil")
	}
	if !strings.Contains(err.Error(), "blocked address") {
		t.Errorf("error = %q, want substring 'blocked address'", err.Error())
	}
}

func TestNewClient_BlocksLiteralLoopback(t *testing.T) {
	c := NewClient()
	_, err := c.Get("http://127.0.0.1:1/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked address") {
		t.Errorf("error = %q, want 'blocked address'", err.Error())
	}
}

func TestNewClient_BlocksLinkLocalMetadata(t *testing.T) {
	// 169.254.169.254 is the cloud metadata endpoint — the most important
	// thing this client must refuse.
	c := NewClient()
	_, err := c.Get("http://169.254.169.254/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked address") {
		t.Errorf("error = %q, want 'blocked address'", err.Error())
	}
}
