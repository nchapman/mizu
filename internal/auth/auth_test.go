package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newAuth(t *testing.T) (*Auth, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, dir
}

func TestNew_FirstRunGeneratesSetupToken(t *testing.T) {
	a, _ := newAuth(t)
	if a.Configured() {
		t.Error("Configured()=true, want false on fresh state")
	}
	if a.SetupToken() == "" {
		t.Error("SetupToken empty on first run")
	}
}

func TestSetPassword_RequiresToken(t *testing.T) {
	a, _ := newAuth(t)
	if err := a.SetPassword("hunter22pw", "wrong-token"); !errors.Is(err, ErrBadSetupToken) {
		t.Errorf("SetPassword wrong token err=%v, want ErrBadSetupToken", err)
	}
}

func TestSetPassword_RejectsShort(t *testing.T) {
	a, _ := newAuth(t)
	if err := a.SetPassword("short", a.SetupToken()); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("err=%v, want ErrPasswordTooShort", err)
	}
}

func TestSetPassword_HappyPathThenLocked(t *testing.T) {
	a, _ := newAuth(t)
	tok := a.SetupToken()
	if err := a.SetPassword("hunter22pw", tok); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if !a.Configured() {
		t.Error("Configured()=false after SetPassword")
	}
	if a.SetupToken() != "" {
		t.Error("SetupToken should clear after configuration")
	}
	// Second call must refuse, and refuse using the original token.
	if err := a.SetPassword("another22pw", tok); !errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("second SetPassword err=%v, want ErrAlreadyConfigured", err)
	}
}

func TestVerify(t *testing.T) {
	a, _ := newAuth(t)
	if a.Verify("anything") {
		t.Error("Verify on unconfigured returned true")
	}
	if err := a.SetPassword("hunter22pw", a.SetupToken()); err != nil {
		t.Fatal(err)
	}
	if !a.Verify("hunter22pw") {
		t.Error("Verify(correct)=false")
	}
	if a.Verify("wrong-password") {
		t.Error("Verify(wrong)=true")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	a1, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a1.SetPassword("hunter22pw", a1.SetupToken()); err != nil {
		t.Fatal(err)
	}
	// Re-instantiate over the same dir.
	a2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a2.Configured() {
		t.Error("Configured()=false after reload")
	}
	if a2.SetupToken() != "" {
		t.Error("SetupToken should be empty after reload")
	}
	if !a2.Verify("hunter22pw") {
		t.Error("Verify failed after reload")
	}
}

func TestNew_RejectsCorruptAuthJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestNew_RejectsEmptyHash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"hash":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("expected error for empty hash")
	}
}

func TestSessions_CreateValidateDestroy(t *testing.T) {
	a, _ := newAuth(t)
	t1, err := a.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	t2, err := a.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if t1 == t2 {
		t.Error("CreateSession returned duplicate tokens")
	}
	if !a.ValidateSession(t1) {
		t.Error("ValidateSession(fresh)=false")
	}
	if a.ValidateSession("") {
		t.Error("ValidateSession(empty)=true")
	}
	if a.ValidateSession("not-a-token") {
		t.Error("ValidateSession(unknown)=true")
	}
	a.DestroySession(t1)
	if a.ValidateSession(t1) {
		t.Error("ValidateSession after destroy=true")
	}
	// DestroySession on empty/unknown is a no-op.
	a.DestroySession("")
	a.DestroySession("nope")
}

func TestSessions_ExpiredSessionRejected(t *testing.T) {
	a, _ := newAuth(t)
	tok, err := a.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.sessions[tok] = session{expires: time.Now().Add(-time.Second)}
	a.mu.Unlock()
	if a.ValidateSession(tok) {
		t.Error("expired session validated")
	}
	// Eviction is deferred to ReapSessions; the entry may still be in
	// the map at this point — that's fine, it's already rejected by
	// ValidateSession. Mimic one ReapSessions tick and confirm it
	// drops the expired entry.
	a.mu.Lock()
	for k, s := range a.sessions {
		if time.Now().After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()
	a.mu.RLock()
	_, present := a.sessions[tok]
	a.mu.RUnlock()
	if present {
		t.Error("expired session not removed by reap")
	}
}

func TestStatus(t *testing.T) {
	a, _ := newAuth(t)
	configured, authed := a.Status("")
	if configured || authed {
		t.Errorf("fresh Status=(%v,%v), want (false,false)", configured, authed)
	}
	if err := a.SetPassword("hunter22pw", a.SetupToken()); err != nil {
		t.Fatal(err)
	}
	configured, authed = a.Status("")
	if !configured || authed {
		t.Errorf("after setup with no token Status=(%v,%v), want (true,false)", configured, authed)
	}
	tok, err := a.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	configured, authed = a.Status(tok)
	if !configured || !authed {
		t.Errorf("with valid session Status=(%v,%v), want (true,true)", configured, authed)
	}
}

func TestSetCookieAndClearCookie(t *testing.T) {
	w := httptest.NewRecorder()
	SetCookie(w, "abc123", false)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName || c.Value != "abc123" {
		t.Errorf("name=%q value=%q", c.Name, c.Value)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("flags wrong: HttpOnly=%v SameSite=%v", c.HttpOnly, c.SameSite)
	}
	if c.Secure {
		t.Error("Secure=true on plain-HTTP cookie")
	}
	if c.MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("MaxAge=%d, want %d", c.MaxAge, int(sessionTTL.Seconds()))
	}

	// secure=true should propagate to the cookie.
	w = httptest.NewRecorder()
	SetCookie(w, "abc123", true)
	if !w.Result().Cookies()[0].Secure {
		t.Error("Secure=false when secure=true was passed")
	}

	w = httptest.NewRecorder()
	ClearCookie(w, false)
	cookies = w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Errorf("ClearCookie should set MaxAge<0, got %+v", cookies)
	}
}

func TestMiddleware(t *testing.T) {
	a, _ := newAuth(t)
	called := false
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	// No cookie → 401.
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie code=%d, want 401", w.Code)
	}
	if called {
		t.Error("handler called without auth")
	}

	// Bad cookie → 401.
	req = httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "garbage"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad cookie code=%d, want 401", w.Code)
	}

	// Valid session → passes through.
	tok, err := a.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("valid session code=%d, want 204", w.Code)
	}
	if !called {
		t.Error("handler not called with valid session")
	}
}
