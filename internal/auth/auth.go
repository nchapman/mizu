// Package auth handles user accounts, sessions, and login lockout for
// the admin UI. All state lives in the shared SQLite DB owned by
// internal/db — there is no on-disk auth file and no in-memory session
// map. A handful of family members can share one instance; everyone
// is equal, posts publish under the configured site author regardless
// of which user wrote them.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"strings"
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
	// against credential brute-forcing on top of the per-IP HTTP rate
	// limit. Keyed by email so failed attempts against a non-existent
	// account still rate-limit and don't leak account existence by
	// behavior.
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute
)

var (
	ErrAlreadyConfigured = errors.New("auth already configured")
	ErrNotConfigured     = errors.New("auth not configured")
	ErrPasswordTooShort  = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	ErrInvalidEmail      = errors.New("invalid email address")
	ErrInvalidLogin      = errors.New("invalid email or password")
	ErrInvalidPassword   = errors.New("invalid password")
	ErrBadSetupToken     = errors.New("invalid setup token")
	ErrEmailTaken        = errors.New("email already in use")
	ErrUserNotFound      = errors.New("user not found")
	ErrLastUser          = errors.New("cannot delete the last remaining user")
)

// User is the public view of a row in the users table.
type User struct {
	ID          int64
	Email       string
	DisplayName string
	CreatedAt   time.Time
	LastLoginAt time.Time // zero if never logged in
}

// Service is the auth API. Construct one per process via New, sharing
// the *sql.DB with the rest of the app.
type Service struct {
	db *sql.DB

	mu         sync.Mutex
	setupToken string // non-empty only while no users exist

	// Overridable for tests; production uses time.Now.
	now func() time.Time
}

type ctxKey struct{}

// dummyHash is bcrypt("placeholder", cost=12). Used to spend roughly
// the same CPU on login attempts against unknown emails so timing
// doesn't leak account existence. Generated once at init.
var dummyHash []byte

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcryptCost)
	if err != nil {
		// Genuinely impossible from in-process bcrypt; fail loud if it ever does.
		panic("auth: failed to seed dummy bcrypt hash: " + err.Error())
	}
	dummyHash = h
}

// New constructs a Service over the given DB. If the users table is
// empty it generates a one-time setup token and logs it via the
// caller (caller checks SetupToken() after New). The token lives in
// memory only — restarting before setup just mints a fresh one.
func New(db *sql.DB) (*Service, error) {
	s := &Service{db: db, now: time.Now}
	configured, err := s.Configured(context.Background())
	if err != nil {
		return nil, err
	}
	if !configured {
		tok, err := randomToken()
		if err != nil {
			return nil, err
		}
		s.setupToken = tok
	}
	return s, nil
}

// Configured returns true if at least one user exists. The SPA hits
// /api/me before login to decide whether to render setup or login.
func (s *Service) Configured(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return n > 0, nil
}

// SetupToken returns the one-time first-run token, or "" if any user
// already exists. Print this at startup so the operator can paste it
// into the setup form — without it, a stranger who beats the operator
// to /setup can lock them out by creating their own first account.
func (s *Service) SetupToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupToken
}

// Setup creates the first user from the one-time token. Refuses if
// any user already exists. Subsequent users are added via CreateUser
// behind the authenticated /api/users endpoint.
//
// The token is *claimed* (zeroed) under the lock before the slow
// bcrypt path runs, so two concurrent Setup calls with the same valid
// token can't both pass the check. If insertUser fails (e.g. email
// invalid at the DB layer), the token is restored so the operator can
// try again without restarting the process.
func (s *Service) Setup(ctx context.Context, token, email, password, displayName string) (*User, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	expected := s.setupToken
	if expected == "" {
		s.mu.Unlock()
		return nil, ErrAlreadyConfigured
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		s.mu.Unlock()
		return nil, ErrBadSetupToken
	}
	s.setupToken = "" // claim before releasing the lock
	s.mu.Unlock()

	user, err := s.insertUser(ctx, email, password, displayName)
	if err != nil {
		// Restore the token so the operator can retry. Skip if another
		// goroutine has already won the race and inserted a user — at
		// that point Configured() is true and Setup must refuse anyway.
		configured, cerr := s.Configured(ctx)
		if cerr == nil && !configured {
			s.mu.Lock()
			s.setupToken = expected
			s.mu.Unlock()
		}
		return nil, err
	}
	return user, nil
}

// CreateUser adds a new account. Called from the authenticated
// /api/users endpoint so an existing user can invite a family member.
// Returns ErrEmailTaken on UNIQUE conflict.
func (s *Service) CreateUser(ctx context.Context, email, password, displayName string) (*User, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	return s.insertUser(ctx, email, password, displayName)
}

func (s *Service) insertUser(ctx context.Context, email, password, displayName string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hash: %w", err)
	}
	now := s.now().UTC().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (email, display_name, password_hash, created_at)
		VALUES (?, ?, ?, ?)`,
		email, strings.TrimSpace(displayName), string(hash), now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{
		ID:          id,
		Email:       email,
		DisplayName: strings.TrimSpace(displayName),
		CreatedAt:   time.Unix(now, 0).UTC(),
	}, nil
}

// ListUsers returns all accounts ordered by creation time.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, display_name, created_at, COALESCE(last_login_at, 0)
		FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created, last int64
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &created, &last); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0).UTC()
		if last > 0 {
			u.LastLoginAt = time.Unix(last, 0).UTC()
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account. Refuses to delete the last remaining
// user so the operator can never get locked out by an accidental DELETE.
// Cascading FK drops the user's sessions.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Check existence first so a missing id surfaces ErrUserNotFound
	// even when only one user remains. Otherwise the operator can't
	// tell apart "you can't delete the last user" from "that id was
	// already gone."
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastUser
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ChangePassword updates a user's password. Verifies the existing
// password first so a stolen session can't silently rotate credentials.
//
// All of the user's sessions are evicted on success, including the
// caller's own — this is the primary incident-response action available
// to a user who suspects a token has been stolen, and a quiet rotation
// that left every existing token valid would defeat the purpose. The
// caller's UI will see the next request return 401 and bounce to login.
func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return ErrInvalidPassword
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, string(newHash), userID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// Login verifies credentials and tracks per-email failures. Returns the
// user on success. On lockout, lockedUntil is non-zero — callers should
// surface a 429 with Retry-After. On any other failure, returns
// ErrInvalidLogin (constant message — never leak whether the email
// exists).
func (s *Service) Login(ctx context.Context, email, password string) (*User, time.Time, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		// Normalize errors are an obvious "bad format" — still bump the
		// counter for the raw input so an attacker spraying badly-formed
		// inputs trips the same gate.
		normalized = strings.ToLower(strings.TrimSpace(email))
	}

	// Lockout gate. Return ErrInvalidLogin alongside the unlock time so
	// the handler can read both: until != zero ⇒ surface 429+Retry-After;
	// otherwise ⇒ 401.
	if locked, until, err := s.checkLock(ctx, normalized); err != nil {
		return nil, time.Time{}, err
	} else if locked {
		return nil, until, ErrInvalidLogin
	}

	// Find the user. If missing, run bcrypt against a dummy hash so the
	// timing doesn't reveal account existence.
	var (
		userID  int64
		display string
		created int64
		lastLog sql.NullInt64
		hash    string
		found   bool
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT id, display_name, created_at, last_login_at, password_hash
		FROM users WHERE email = ?`, normalized,
	).Scan(&userID, &display, &created, &lastLog, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		found = false
	case err != nil:
		return nil, time.Time{}, err
	default:
		found = true
	}

	compareHash := []byte(hash)
	if !found {
		compareHash = dummyHash
	}
	bcryptOK := bcrypt.CompareHashAndPassword(compareHash, []byte(password)) == nil
	if !found || !bcryptOK {
		until, err := s.recordFailure(ctx, normalized)
		if err != nil {
			return nil, time.Time{}, err
		}
		return nil, until, ErrInvalidLogin
	}

	if err := s.clearFailures(ctx, normalized); err != nil {
		return nil, time.Time{}, err
	}
	now := s.now().UTC().Unix()
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID); err != nil {
		return nil, time.Time{}, err
	}
	u := &User{
		ID:          userID,
		Email:       normalized,
		DisplayName: display,
		CreatedAt:   time.Unix(created, 0).UTC(),
		LastLoginAt: time.Unix(now, 0).UTC(),
	}
	return u, time.Time{}, nil
}

func (s *Service) checkLock(ctx context.Context, email string) (locked bool, until time.Time, err error) {
	var lockedUntil sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT locked_until FROM login_attempts WHERE email = ?`, email).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	if !lockedUntil.Valid {
		return false, time.Time{}, nil
	}
	t := time.Unix(lockedUntil.Int64, 0)
	if s.now().Before(t) {
		return true, t, nil
	}
	// Lockout has expired. Clear the row so the failed_count resets to
	// zero — otherwise the very next wrong password re-trips the lock,
	// letting an attacker keep an account permanently locked with one
	// probe per lockout window.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE email = ?`, email); err != nil {
		return false, time.Time{}, err
	}
	return false, time.Time{}, nil
}

func (s *Service) recordFailure(ctx context.Context, email string) (time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now()
	var failed int
	var lastFailed int64
	err = tx.QueryRowContext(ctx,
		`SELECT failed_count, last_failed_at FROM login_attempts WHERE email = ?`, email,
	).Scan(&failed, &lastFailed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, err
	}
	// Age out a stale counter: if it's been longer than the lockout
	// window since the last failure, the campaign has gone cold and
	// the counter should start fresh. Without this, four wrong
	// passwords today + one wrong password next month = instant
	// lockout, which is hostile to the operator without making the
	// system more secure.
	if lastFailed > 0 && now.Unix()-lastFailed > int64(lockoutDuration.Seconds()) {
		failed = 0
	}
	failed++

	var lockedUntil sql.NullInt64
	var until time.Time
	if failed >= maxFailedAttempts {
		until = now.Add(lockoutDuration)
		lockedUntil = sql.NullInt64{Int64: until.Unix(), Valid: true}
		log.Printf("auth: locking out %q after %d failed attempts; unlock at %s",
			email, failed, until.Format(time.RFC3339))
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO login_attempts (email, failed_count, last_failed_at, locked_until)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			failed_count   = excluded.failed_count,
			last_failed_at = excluded.last_failed_at,
			locked_until   = excluded.locked_until`,
		email, failed, now.Unix(), lockedUntil,
	)
	if err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

func (s *Service) clearFailures(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE email = ?`, email)
	return err
}

// CreateSession inserts a fresh session row bound to userID and
// returns its opaque token. Caller sets the cookie via SetCookie.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Unix()
	expires := s.now().Add(sessionTTL).UTC().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (token, user_id, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		token, userID, now, now, expires,
	)
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return token, nil
}

// Lookup returns the user behind a session token, or nil if the token
// is unknown or expired. Used by Middleware and /api/me.
func (s *Service) Lookup(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	var (
		u       User
		expires int64
		created int64
		lastLog sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.display_name, u.created_at, u.last_login_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ?`, token,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &created, &lastLog, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.now().Unix() >= expires {
		// Defer eviction to ReapSessions; treat as invalid here.
		return nil, nil
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	if lastLog.Valid {
		u.LastLoginAt = time.Unix(lastLog.Int64, 0).UTC()
	}
	return &u, nil
}

// DestroySession removes the session row. Idempotent.
func (s *Service) DestroySession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// ReapSessions runs until ctx is cancelled, deleting expired sessions
// once an hour. Without this, abandoned tokens (browser closed without
// logout) accumulate in the sessions table forever.
func (s *Service) ReapSessions(ctx context.Context) {
	tk := time.NewTicker(time.Hour)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			now := s.now().Unix()
			if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
				log.Printf("auth: reap sessions: %v", err)
			}
			// Drop stale login_attempts rows. Without this, an attacker
			// spraying novel emails (which never trip per-email lockout
			// because each is a first attempt) grows the table one row
			// per probe. Aging out at 2x the lockout window keeps real
			// lockouts intact — checkLock clears its own counter on the
			// first attempt after the window, so any row older than
			// 2*window is guaranteed dead state.
			cutoff := now - int64(2*lockoutDuration.Seconds())
			if _, err := s.db.ExecContext(ctx,
				`DELETE FROM login_attempts
				 WHERE last_failed_at < ?
				   AND (locked_until IS NULL OR locked_until < ?)`,
				cutoff, now,
			); err != nil {
				log.Printf("auth: reap login_attempts: %v", err)
			}
		}
	}
}

// UserFrom returns the User attached to ctx by Middleware, or nil
// if the request didn't pass through Middleware.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Middleware enforces authentication. Unauth'd requests get 401 with
// no body — the SPA handles redirect to login. On success, the User
// is attached to the request context for handlers that want it.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := s.Lookup(r.Context(), c.Value)
		if err != nil {
			log.Printf("auth: lookup session: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SetCookie writes the session cookie.
//
// SameSite=Strict closes the CSRF window that Lax leaves open: under
// Lax, browsers still send the cookie on top-level POST navigations
// (the bypass that a cross-site auto-submitting form exploits). Strict
// withholds the cookie on every cross-site request. The behavioral cost
// — clicking a link to /admin from another site lands you unauthed —
// is fine for an admin UI; the operator just logs in. HttpOnly always
// set; Secure when serving HTTPS so the cookie can't leak over the
// first-visit HTTP→HTTPS redirect before HSTS caches.
func SetCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
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
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func validatePassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	return nil
}

func normalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", ErrInvalidEmail
	}
	// ParseAddress accepts the "Name <addr>" form too; we want just the
	// addr-spec for storage. Lowercase the local-and-domain so login is
	// case-insensitive (matches the UNIQUE COLLATE NOCASE column).
	return strings.ToLower(addr.Address), nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint
// failure. modernc.org/sqlite (and mattn/go-sqlite3) both produce the
// "UNIQUE constraint failed" prefix; we string-sniff rather than depend
// on a driver-internal error type so the mapping survives a future
// driver swap. Verified against modernc v1.x.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
