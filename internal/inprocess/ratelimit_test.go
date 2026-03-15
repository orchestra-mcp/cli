package inprocess

import (
	"strings"
	"testing"
	"time"
)

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := &rateLimiter{
		limit:   5,
		window:  time.Minute,
		callers: make(map[string]*callerBucket),
	}

	for i := 0; i < 5; i++ {
		if err := rl.allow("test-caller"); err != nil {
			t.Fatalf("call %d should be allowed: %v", i+1, err)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := &rateLimiter{
		limit:   3,
		window:  time.Minute,
		callers: make(map[string]*callerBucket),
	}

	// Use up the limit.
	for i := 0; i < 3; i++ {
		rl.allow("test-caller")
	}

	// 4th call should be blocked.
	err := rl.allow("test-caller")
	if err == nil {
		t.Fatal("expected rate limit error")
	}

	rlErr, ok := err.(*rateLimitError)
	if !ok {
		t.Fatalf("expected *rateLimitError, got %T", err)
	}
	if rlErr.RetryAfter <= 0 {
		t.Errorf("RetryAfter should be positive, got %v", rlErr.RetryAfter)
	}
}

func TestRateLimiter_PerCallerIsolation(t *testing.T) {
	rl := &rateLimiter{
		limit:   2,
		window:  time.Minute,
		callers: make(map[string]*callerBucket),
	}

	// Fill up caller A.
	rl.allow("caller-a")
	rl.allow("caller-a")

	// Caller B should still be allowed.
	if err := rl.allow("caller-b"); err != nil {
		t.Fatalf("caller-b should not be affected by caller-a: %v", err)
	}

	// Caller A should be blocked.
	if err := rl.allow("caller-a"); err == nil {
		t.Fatal("caller-a should be rate limited")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := &rateLimiter{
		limit:   2,
		window:  50 * time.Millisecond,
		callers: make(map[string]*callerBucket),
	}

	rl.allow("test")
	rl.allow("test")

	// Should be blocked now.
	if err := rl.allow("test"); err == nil {
		t.Fatal("should be rate limited")
	}

	// Wait for window to expire.
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again.
	if err := rl.allow("test"); err != nil {
		t.Fatalf("should be allowed after window expiry: %v", err)
	}
}

func TestRateLimitError_Message(t *testing.T) {
	err := &rateLimitError{RetryAfter: 5 * time.Second}
	msg := err.Error()
	if !strings.Contains(msg, "rate limit exceeded") {
		t.Errorf("expected 'rate limit exceeded' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "retry after") {
		t.Errorf("expected 'retry after' in message, got: %s", msg)
	}
}

func TestNewRateLimiter_DefaultLimit(t *testing.T) {
	rl := newRateLimiter()
	if rl.limit != defaultRateLimit {
		t.Errorf("expected default limit %d, got %d", defaultRateLimit, rl.limit)
	}
	if rl.window != rateLimitWindow {
		t.Errorf("expected window %v, got %v", rateLimitWindow, rl.window)
	}
}

func TestNewRateLimiter_EnvOverride(t *testing.T) {
	t.Setenv(rateLimitEnvVar, "50")
	rl := newRateLimiter()
	if rl.limit != 50 {
		t.Errorf("expected limit 50 from env, got %d", rl.limit)
	}
}

func TestNewRateLimiter_InvalidEnv(t *testing.T) {
	t.Setenv(rateLimitEnvVar, "not-a-number")
	rl := newRateLimiter()
	if rl.limit != defaultRateLimit {
		t.Errorf("expected default limit on invalid env, got %d", rl.limit)
	}
}
