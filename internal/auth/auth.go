// Package auth handles single-user password authentication for the admin UI.
//
// State lives in two places:
//
//   - state/auth.json on disk holds the bcrypt password hash. This is the
//     durable record; a fresh checkout with no auth.json triggers the
//     first-run setup flow.
//   - An in-memory session map holds active session tokens. Sessions don't
//     survive a restart by design — the trade-off is one extra login vs. a
//     persisted session table for a single user.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName     = "mizu_session"
	sessionTTL     = 30 * 24 * time.Hour
	MinPasswordLen = 8
	bcryptCost     = 12

	// maxFailedAttempts and lockoutDuration implement defense-in-depth
	// against credential brute-forcing on top of the per-IP rate limit
	// at the HTTP layer. The IP limit handles spray; this handles a
	// patient attacker rotating through proxies.
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute
)

var (
	ErrAlreadyConfigured = errors.New("auth already configured")
	ErrPasswordTooShort  = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	ErrInvalidPassword   = errors.New("invalid password")
	ErrBadSetupToken     = errors.New("invalid setup token")
)

type onDisk struct {
	Hash string `json:"hash"`
}

type session struct {
	expires time.Time
}

type Auth struct {
	path string

	mu         sync.RWMutex
	hash       []byte // empty when not configured
	sessions   map[string]session
	setupToken string // non-empty only while unconfigured; required for SetPassword

	// Failure-tracking state for login lockout. Single-user system, so
	// the counter is global rather than keyed by username.
	failedAttempts int
	lockedUntil    time.Time

	// Overridable for tests; production uses time.Now.
	now func() time.Time
}

// New loads the existing hash from stateDir/auth.json if present. Missing
// file is not an error — it means the system is in first-run state, and
// a one-time setup token is generated to guard the /setup endpoint
// against a hostile pre-emption race on internet-exposed instances.
func New(stateDir string) (*Auth, error) {
	a := &Auth{
		path:     filepath.Join(stateDir, "auth.json"),
		sessions: map[string]session{},
		now:      time.Now,
	}
	b, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		token, err := randomToken()
		if err != nil {
			return nil, err
		}
		a.setupToken = token
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read auth.json: %w", err)
	}
	var d onDisk
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse auth.json: %w", err)
	}
	if d.Hash == "" {
		return nil, errors.New("auth.json present but missing hash")
	}
	a.hash = []byte(d.Hash)
	return a, nil
}

func (a *Auth) Configured() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.hash) > 0
}

// SetupToken returns the one-time first-run token, or "" if already
// configured. Print this at startup so the operator can paste it into
// the setup form — without it, a stranger who beats the operator to
// /setup can lock them out by setting their own password.
func (a *Auth) SetupToken() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.setupToken
}

// Status reports configured + authenticated atomically so the SPA
// doesn't observe a half-mutated state during first-run setup.
func (a *Auth) Status(token string) (configured, authenticated bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	configured = len(a.hash) > 0
	if !configured || token == "" {
		return
	}
	s, ok := a.sessions[token]
	if ok && time.Now().Before(s.expires) {
		authenticated = true
	}
	return
}

// SetPassword writes the initial password hash. Refuses to overwrite an
// existing configuration so a stray /setup request can't lock the user
// out by replacing the password. The token must match the one printed
// at startup; this prevents a hostile party from racing the legitimate
// operator during the first-run window.
func (a *Auth) SetPassword(pw, token string) error {
	if len(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.hash) > 0 {
		return ErrAlreadyConfigured
	}
	if a.setupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.setupToken)) != 1 {
		return ErrBadSetupToken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	if err := writeJSONAtomic(a.path, onDisk{Hash: string(hash)}); err != nil {
		return err
	}
	a.hash = hash
	a.setupToken = "" // one-shot
	return nil
}

func (a *Auth) Verify(pw string) bool {
	a.mu.RLock()
	hash := a.hash
	a.mu.RUnlock()
	if len(hash) == 0 {
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(pw)) == nil
}

// LoginAttempt verifies pw and tracks consecutive failures so a
// patient brute-forcer (rotating IPs to skirt the HTTP rate limit)
// still gets stopped at the auth layer. After maxFailedAttempts
// consecutive failures, further attempts are rejected without
// running bcrypt until lockoutDuration has elapsed.
//
// Successful verification resets the counter. The returned
// lockedUntil is the zero value when the account is not locked.
//
// bcrypt is intentionally run *outside* the mutex so a slow hash
// comparison (~100ms at cost 12) doesn't block ValidateSession on
// every other admin request. If two attempts race past the lockout
// gate, both run bcrypt and both increment failedAttempts on
// failure — the lockout still triggers, just one bcrypt call later.
func (a *Auth) LoginAttempt(pw string) (ok bool, lockedUntil time.Time) {
	a.mu.Lock()
	hash := a.hash
	now := a.now()
	if len(hash) == 0 {
		a.mu.Unlock()
		return false, time.Time{}
	}
	if !a.lockedUntil.IsZero() && now.Before(a.lockedUntil) {
		locked := a.lockedUntil
		a.mu.Unlock()
		return false, locked
	}
	a.mu.Unlock()

	verified := bcrypt.CompareHashAndPassword(hash, []byte(pw)) == nil

	a.mu.Lock()
	defer a.mu.Unlock()
	if verified {
		a.failedAttempts = 0
		a.lockedUntil = time.Time{}
		return true, time.Time{}
	}
	a.failedAttempts++
	if a.failedAttempts >= maxFailedAttempts {
		a.lockedUntil = a.now().Add(lockoutDuration)
		log.Printf("auth: locking out after %d failed attempts; unlock at %s",
			a.failedAttempts, a.lockedUntil.Format(time.RFC3339))
		return false, a.lockedUntil
	}
	return false, time.Time{}
}

// CreateSession returns a fresh opaque token bound to a server-side
// expiry. Callers should set it in a cookie via SetCookie.
func (a *Auth) CreateSession() (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.sessions[token] = session{expires: time.Now().Add(sessionTTL)}
	a.mu.Unlock()
	return token, nil
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// ReapSessions runs until ctx is cancelled, evicting expired session
// tokens once an hour. Without this, abandoned tokens (browser closed
// without logout) sit in the map until process restart.
func (a *Auth) ReapSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.mu.Lock()
			for token, s := range a.sessions {
				if now.After(s.expires) {
					delete(a.sessions, token)
				}
			}
			a.mu.Unlock()
		}
	}
}

// ValidateSession returns true if the token is known and unexpired.
// The fast path takes only the read lock so a slow login (bcrypt
// holding the write lock briefly between snapshot and update) doesn't
// stall every other admin request. Eviction of expired tokens is
// deferred to ReapSessions; until then an expired token is treated as
// invalid here, which is the only behavior callers care about.
func (a *Auth) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(s.expires) {
		return false
	}
	return true
}

func (a *Auth) DestroySession(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// SetCookie writes the session cookie. SameSite=Lax + HttpOnly are
// always set; Secure is set when secure=true, which deployments
// should pass when serving HTTPS so the cookie can never leak in
// cleartext (notably on the first-visit HTTP→HTTPS redirect, before
// HSTS is cached by the browser).
func SetCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Middleware enforces authentication on the wrapped handler. Unauth'd
// requests get 401 with no body — the SPA handles redirect to login.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || !a.ValidateSession(c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	// fsync the file before rename so a power loss after rename can't
	// leave the target path pointing to a zero-length inode. Losing the
	// hash file would lock the operator out — fsync is cheap insurance.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
