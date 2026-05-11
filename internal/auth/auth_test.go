package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/db"
)

func newSvc(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	return s, conn
}

func TestNew_FirstRunOpensSetupWindow(t *testing.T) {
	s, _ := newSvc(t)
	ok, err := s.Configured(context.Background())
	if err != nil || ok {
		t.Errorf("Configured=(%v,%v), want (false,nil)", ok, err)
	}
	win, err := s.Window(context.Background())
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if !win.Open {
		t.Error("Window.Open=false on first run")
	}
	if win.ExpiresAt.IsZero() {
		t.Error("Window.ExpiresAt zero on first run")
	}
}

func TestSetup_RejectsShortPassword(t *testing.T) {
	s, _ := newSvc(t)
	_, err := s.Setup(context.Background(), "a@b.com", "short", "Alice")
	if !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("err=%v, want ErrPasswordTooShort", err)
	}
}

func TestSetup_RejectsInvalidEmail(t *testing.T) {
	s, _ := newSvc(t)
	_, err := s.Setup(context.Background(), "not an email", "hunter22pw", "")
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("err=%v, want ErrInvalidEmail", err)
	}
}

func TestSetup_HappyPathThenLocked(t *testing.T) {
	s, _ := newSvc(t)
	u, err := s.Setup(context.Background(), "Alice@example.COM", "hunter22pw", "Alice")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email not normalized: %q", u.Email)
	}
	if u.DisplayName != "Alice" {
		t.Errorf("display name=%q", u.DisplayName)
	}
	win, err := s.Window(context.Background())
	if err != nil {
		t.Fatalf("Window after setup: %v", err)
	}
	if win.Open {
		t.Error("Window.Open=true after first user created")
	}
	// Second Setup must fail.
	_, err = s.Setup(context.Background(), "b@example.com", "hunter22pw", "")
	if !errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("second Setup err=%v, want ErrAlreadyConfigured", err)
	}
}

func TestSetup_RefusesAfterWindowExpires(t *testing.T) {
	s, _ := newSvc(t)
	fakeNow := time.Now().Add(SetupWindowDuration + time.Minute)
	s.now = func() time.Time { return fakeNow }
	_, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", "")
	if !errors.Is(err, ErrSetupWindowClosed) {
		t.Errorf("err=%v, want ErrSetupWindowClosed", err)
	}
}

func TestWindow_ResetsOnRestart(t *testing.T) {
	// The window is intentionally in-memory: a process restart reopens
	// it. Recovery for an operator who misses the window is to restart
	// the server, which is a strong "I have host access" signal — and
	// an attacker who can restart already has root and wins anyway.
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	s1, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	w1, _ := s1.Window(context.Background())

	// Simulate a restart by walking forward past the original window
	// and constructing a new Service over the same DB.
	time.Sleep(2 * time.Millisecond)
	s2, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	w2, _ := s2.Window(context.Background())
	if !w2.ExpiresAt.After(w1.ExpiresAt) {
		t.Errorf("window did not advance across restart: %v vs %v", w1.ExpiresAt, w2.ExpiresAt)
	}
}

func TestPersistence_AcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// Reopen.
	conn2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	s2, err := New(conn2)
	if err != nil {
		t.Fatal(err)
	}
	configured, _ := s2.Configured(context.Background())
	if !configured {
		t.Error("Configured=false after reopen")
	}
	win, _ := s2.Window(context.Background())
	if win.Open {
		t.Error("Window should be closed after reopen with existing user")
	}
	u, _, err := s2.Login(context.Background(), "a@b.com", "hunter22pw")
	if err != nil {
		t.Fatalf("Login after reopen: %v", err)
	}
	if u.Email != "a@b.com" {
		t.Errorf("u.Email=%q", u.Email)
	}
}

func TestLogin_WrongPasswordReturnsGenericError(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	_, until, err := s.Login(context.Background(), "a@b.com", "nope")
	if !errors.Is(err, ErrInvalidLogin) {
		t.Errorf("err=%v, want ErrInvalidLogin", err)
	}
	if !until.IsZero() {
		t.Errorf("until=%v on first failure, want zero", until)
	}
}

func TestLogin_UnknownEmailReturnsSameError(t *testing.T) {
	s, _ := newSvc(t)
	// No setup; users table is empty.
	_, _, err := s.Login(context.Background(), "ghost@example.com", "whatever123")
	if !errors.Is(err, ErrInvalidLogin) {
		t.Errorf("err=%v, want ErrInvalidLogin (must not leak account existence)", err)
	}
}

func TestLogin_HappyPathClearsFailures(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", "A"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailedAttempts-1; i++ {
		if _, _, err := s.Login(context.Background(), "a@b.com", "nope"); !errors.Is(err, ErrInvalidLogin) {
			t.Fatalf("attempt %d: err=%v", i, err)
		}
	}
	u, until, err := s.Login(context.Background(), "a@b.com", "hunter22pw")
	if err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if !until.IsZero() {
		t.Errorf("until=%v on success", until)
	}
	if u.LastLoginAt.IsZero() {
		t.Error("LastLoginAt not updated on success")
	}
	// Counter cleared: should be able to fail maxFailedAttempts more
	// times before getting locked.
	for i := 0; i < maxFailedAttempts-1; i++ {
		if _, _, err := s.Login(context.Background(), "a@b.com", "nope"); !errors.Is(err, ErrInvalidLogin) {
			t.Fatalf("post-reset attempt %d: err=%v", i, err)
		}
	}
}

func TestLogin_LocksAfterMaxFailures(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailedAttempts-1; i++ {
		_, until, _ := s.Login(context.Background(), "a@b.com", "nope")
		if !until.IsZero() {
			t.Fatalf("premature lock at attempt %d", i)
		}
	}
	_, until, _ := s.Login(context.Background(), "a@b.com", "nope")
	if until.IsZero() {
		t.Fatal("expected lockout after max failures")
	}
	// Even the right password is rejected while locked.
	_, until2, err := s.Login(context.Background(), "a@b.com", "hunter22pw")
	if err == nil {
		t.Fatal("locked account accepted correct password")
	}
	if until2.IsZero() {
		t.Fatal("expected until to remain set during lockout")
	}
}

func TestLogin_LockoutKeyedByEmail(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "alice@example.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(context.Background(), "bob@example.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	// Burn Alice's lockout.
	for i := 0; i < maxFailedAttempts; i++ {
		s.Login(context.Background(), "alice@example.com", "nope")
	}
	if _, until, _ := s.Login(context.Background(), "alice@example.com", "hunter22pw"); until.IsZero() {
		t.Fatal("Alice should be locked")
	}
	// Bob is independent.
	if _, until, err := s.Login(context.Background(), "bob@example.com", "hunter22pw"); err != nil || !until.IsZero() {
		t.Errorf("Bob locked by Alice's failures: err=%v until=%v", err, until)
	}
}

// Regression: once a lockout expires, the failed-attempt counter must
// reset so a single subsequent wrong password doesn't immediately
// re-trip the lock. Without this, an attacker can keep an account
// permanently locked with one probe per lockoutDuration window.
func TestLogin_LockoutResetsCounterAfterExpiry(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	fakeNow := time.Now()
	s.now = func() time.Time { return fakeNow }
	for i := 0; i < maxFailedAttempts; i++ {
		s.Login(context.Background(), "a@b.com", "nope")
	}
	// Advance past the lockout window.
	fakeNow = fakeNow.Add(lockoutDuration + time.Second)

	// One more wrong password — must NOT re-lock immediately.
	_, until, _ := s.Login(context.Background(), "a@b.com", "nope")
	if !until.IsZero() {
		t.Fatalf("counter not reset after lockout expiry: single failure re-locked (until=%v)", until)
	}
}

func TestLogin_UnlocksAfterDuration(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	fakeNow := time.Now()
	s.now = func() time.Time { return fakeNow }
	for i := 0; i < maxFailedAttempts; i++ {
		s.Login(context.Background(), "a@b.com", "nope")
	}
	// Advance past the lockout window.
	fakeNow = fakeNow.Add(lockoutDuration + time.Second)
	u, until, err := s.Login(context.Background(), "a@b.com", "hunter22pw")
	if err != nil {
		t.Fatalf("expected success after lockout expires: %v", err)
	}
	if u == nil || !until.IsZero() {
		t.Errorf("u=%v until=%v", u, until)
	}
}

func TestCreateUser_DuplicateEmail(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateUser(context.Background(), "A@B.COM", "hunter22pw", "")
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("err=%v, want ErrEmailTaken (case-insensitive)", err)
	}
}

func TestListUsers_OrderedByCreated(t *testing.T) {
	s, _ := newSvc(t)
	fakeNow := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return fakeNow }
	if _, err := s.Setup(context.Background(), "first@example.com", "hunter22pw", "First"); err != nil {
		t.Fatal(err)
	}
	fakeNow = fakeNow.Add(time.Hour)
	if _, err := s.CreateUser(context.Background(), "second@example.com", "hunter22pw", "Second"); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Email != "first@example.com" || users[1].Email != "second@example.com" {
		t.Errorf("users=%+v", users)
	}
}

func TestDeleteUser_RefusesLastUser(t *testing.T) {
	s, _ := newSvc(t)
	u, err := s.Setup(context.Background(), "a@b.com", "hunter22pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(context.Background(), u.ID); !errors.Is(err, ErrLastUser) {
		t.Errorf("err=%v, want ErrLastUser", err)
	}
}

func TestDeleteUser_CascadesSessions(t *testing.T) {
	s, _ := newSvc(t)
	a, _ := s.Setup(context.Background(), "a@b.com", "hunter22pw", "")
	b, err := s.CreateUser(context.Background(), "b@b.com", "hunter22pw", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateSession(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Lookup(context.Background(), tok); u != nil {
		t.Errorf("session survived user delete: %+v", u)
	}
	if err := s.DeleteUser(context.Background(), 9999); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("delete missing err=%v, want ErrUserNotFound", err)
	}
	// a still exists.
	if err := s.DeleteUser(context.Background(), a.ID); !errors.Is(err, ErrLastUser) {
		t.Errorf("a deletable after b removed; err=%v", err)
	}
}

func TestChangePassword(t *testing.T) {
	s, _ := newSvc(t)
	u, _ := s.Setup(context.Background(), "a@b.com", "hunter22pw", "")
	if err := s.ChangePassword(context.Background(), u.ID, "wrong", "newpassword22"); !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("wrong old: err=%v", err)
	}
	if err := s.ChangePassword(context.Background(), u.ID, "hunter22pw", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short new: err=%v", err)
	}
	if err := s.ChangePassword(context.Background(), u.ID, "hunter22pw", "newpassword22"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, _, err := s.Login(context.Background(), "a@b.com", "hunter22pw"); !errors.Is(err, ErrInvalidLogin) {
		t.Error("old password still works")
	}
	if _, _, err := s.Login(context.Background(), "a@b.com", "newpassword22"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestSessions_LifecycleAndReap(t *testing.T) {
	s, conn := newSvc(t)
	u, _ := s.Setup(context.Background(), "a@b.com", "hunter22pw", "")
	t1, err := s.CreateSession(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.CreateSession(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if t1 == t2 {
		t.Error("CreateSession returned duplicate tokens")
	}
	got, err := s.Lookup(context.Background(), t1)
	if err != nil || got == nil || got.ID != u.ID {
		t.Errorf("Lookup fresh: got=%+v err=%v", got, err)
	}
	if u, _ := s.Lookup(context.Background(), ""); u != nil {
		t.Error("Lookup(empty) returned a user")
	}
	if u, _ := s.Lookup(context.Background(), "garbage"); u != nil {
		t.Error("Lookup(unknown) returned a user")
	}
	if err := s.DestroySession(context.Background(), t1); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Lookup(context.Background(), t1); u != nil {
		t.Error("Lookup after destroy returned a user")
	}
	// Idempotent.
	if err := s.DestroySession(context.Background(), t1); err != nil {
		t.Errorf("destroy after destroy: %v", err)
	}
	if err := s.DestroySession(context.Background(), ""); err != nil {
		t.Errorf("destroy empty: %v", err)
	}

	// Force t2 expired by rewriting expires_at, then run one reaper
	// tick worth of work.
	_, err = conn.W.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(-time.Second).Unix(), t2)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Lookup(context.Background(), t2); u != nil {
		t.Error("expired session validated")
	}
	_, err = conn.W.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	var n int
	conn.R.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, t2).Scan(&n)
	if n != 0 {
		t.Errorf("expired session not removed by reap: n=%d", n)
	}
}

func TestMiddleware(t *testing.T) {
	s, _ := newSvc(t)
	u, _ := s.Setup(context.Background(), "a@b.com", "hunter22pw", "Alice")
	var seen *User
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = UserFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	// No cookie → 401.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie code=%d", w.Code)
	}
	if seen != nil {
		t.Error("handler called without auth")
	}

	// Bad cookie → 401.
	req := httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "garbage"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad cookie code=%d", w.Code)
	}

	// Valid session → passes through with user attached.
	tok, _ := s.CreateSession(context.Background(), u.ID)
	req = httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("valid session code=%d", w.Code)
	}
	if seen == nil || seen.ID != u.ID || seen.Email != "a@b.com" {
		t.Errorf("UserFrom=%+v", seen)
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
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("flags wrong: HttpOnly=%v SameSite=%v", c.HttpOnly, c.SameSite)
	}
	if c.Secure {
		t.Error("Secure=true on plain-HTTP cookie")
	}
	if c.MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("MaxAge=%d, want %d", c.MaxAge, int(sessionTTL.Seconds()))
	}
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

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"a@b.com", "a@b.com", true},
		{"  A@B.COM  ", "a@b.com", true},
		{"", "", false},
		{"not-an-email", "", false},
		{"Alice <alice@example.com>", "alice@example.com", true}, // Go's mail.ParseAddress accepts name form
	}
	for _, c := range cases {
		got, err := normalizeEmail(c.in)
		if (err == nil) != c.ok {
			t.Errorf("normalizeEmail(%q) err=%v ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("normalizeEmail(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}
