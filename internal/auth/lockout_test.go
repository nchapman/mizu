package auth

import (
	"testing"
	"time"
)

func TestLoginAttempt_HappyPathResetsCounter(t *testing.T) {
	a, _ := newAuth(t)
	if err := a.SetPassword("hunter2pw", a.SetupToken()); err != nil {
		t.Fatal(err)
	}
	// A few failures, then a success: counter should reset and the
	// account should never lock.
	for i := 0; i < maxFailedAttempts-1; i++ {
		ok, _ := a.LoginAttempt("nope")
		if ok {
			t.Fatalf("attempt %d: ok=true with wrong password", i)
		}
	}
	ok, lockedUntil := a.LoginAttempt("hunter2pw")
	if !ok {
		t.Fatal("correct password rejected")
	}
	if !lockedUntil.IsZero() {
		t.Fatalf("lockedUntil=%v on success", lockedUntil)
	}
	if a.failedAttempts != 0 {
		t.Errorf("failedAttempts=%d after success, want 0", a.failedAttempts)
	}
}

func TestLoginAttempt_LocksAfterMaxFailures(t *testing.T) {
	a, _ := newAuth(t)
	if err := a.SetPassword("hunter2pw", a.SetupToken()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailedAttempts-1; i++ {
		ok, lockedUntil := a.LoginAttempt("nope")
		if ok || !lockedUntil.IsZero() {
			t.Fatalf("premature lock at attempt %d", i)
		}
	}
	ok, lockedUntil := a.LoginAttempt("nope")
	if ok {
		t.Fatal("ok=true on wrong password")
	}
	if lockedUntil.IsZero() {
		t.Fatal("expected lockout after max failures")
	}

	// Even with the right password, locked means locked.
	ok, lockedUntil = a.LoginAttempt("hunter2pw")
	if ok {
		t.Fatal("locked account accepted correct password")
	}
	if lockedUntil.IsZero() {
		t.Fatal("expected lockedUntil to remain set")
	}
}

func TestLoginAttempt_UnlocksAfterDuration(t *testing.T) {
	a, _ := newAuth(t)
	if err := a.SetPassword("hunter2pw", a.SetupToken()); err != nil {
		t.Fatal(err)
	}
	// Drive the clock so the lockout has expired without sleeping.
	fakeNow := time.Now()
	a.now = func() time.Time { return fakeNow }
	for i := 0; i < maxFailedAttempts; i++ {
		a.LoginAttempt("nope")
	}
	if a.lockedUntil.IsZero() {
		t.Fatal("expected lockout")
	}
	// Jump past the lockout window.
	fakeNow = fakeNow.Add(lockoutDuration + time.Second)
	ok, lockedUntil := a.LoginAttempt("hunter2pw")
	if !ok {
		t.Fatal("expected success after lockout expires")
	}
	if !lockedUntil.IsZero() {
		t.Fatalf("lockedUntil=%v after expiry", lockedUntil)
	}
}
