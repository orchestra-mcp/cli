package inprocess

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultRateLimit = 1000 // calls per window
	rateLimitWindow  = time.Minute
	rateLimitEnvVar  = "ORCHESTRA_RATE_LIMIT"
)

// rateLimiter implements a simple sliding-window rate limiter keyed by caller.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	callers map[string]*callerBucket
}

// callerBucket tracks call timestamps for a single caller within the window.
type callerBucket struct {
	timestamps []time.Time
}

// newRateLimiter creates a rate limiter. It reads the limit from the
// ORCHESTRA_RATE_LIMIT environment variable, falling back to defaultRateLimit.
func newRateLimiter() *rateLimiter {
	limit := defaultRateLimit
	if v := os.Getenv(rateLimitEnvVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	return &rateLimiter{
		limit:   limit,
		window:  rateLimitWindow,
		callers: make(map[string]*callerBucket),
	}
}

// rateLimitError is returned when a caller exceeds the rate limit.
type rateLimitError struct {
	RetryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded, retry after %s", e.RetryAfter.Truncate(time.Millisecond))
}

// allow checks whether the given caller can make a call right now.
// Returns nil if allowed, or a *rateLimitError with a retry hint if not.
func (rl *rateLimiter) allow(caller string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	bucket, ok := rl.callers[caller]
	if !ok {
		bucket = &callerBucket{}
		rl.callers[caller] = bucket
	}

	// Evict expired timestamps.
	valid := bucket.timestamps[:0]
	for _, ts := range bucket.timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}
	bucket.timestamps = valid

	if len(bucket.timestamps) >= rl.limit {
		// Calculate when the oldest entry in the window expires.
		retryAfter := bucket.timestamps[0].Add(rl.window).Sub(now)
		if retryAfter < 0 {
			retryAfter = time.Second
		}
		return &rateLimitError{RetryAfter: retryAfter}
	}

	bucket.timestamps = append(bucket.timestamps, now)
	return nil
}
