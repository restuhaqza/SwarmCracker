// This file owns the per-token request rate limit: a token bucket keyed by the
// authenticated identity, with the contract's 10 req/s + burst 20 defaults.
package apihttp

import (
	"math"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// DefaultRatePerSecond and DefaultRateBurst are the contract defaults.
	DefaultRatePerSecond = 10.0
	DefaultRateBurst     = 20
)

// RateLimiter is a keyed token bucket. It is safe for concurrent use; now is
// injectable so tests do not sleep.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a limiter. A non-positive rate or burst means the
// contract defaults.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	if perSecond <= 0 {
		perSecond = DefaultRatePerSecond
	}
	if burst <= 0 {
		burst = DefaultRateBurst
	}
	return &RateLimiter{
		buckets: make(map[string]*rateBucket),
		rate:    perSecond,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key. A denied request gets the duration until the
// next token; this is the only place the bucket is advanced.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &rateBucket{tokens: l.burst, last: now}
		l.buckets[key] = bucket
	}

	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.rate)
		bucket.last = now
	}
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}

	seconds := (1 - bucket.tokens) / l.rate
	if seconds < 0 {
		seconds = 0
	}
	return false, time.Duration(seconds * float64(time.Second))
}

// rateLimitError is the 429 document. Retry-After is set by the middleware; the
// details carry the same hint in milliseconds for JSON clients.
func rateLimitError(retryAfter time.Duration, limiter *RateLimiter) error {
	ms := retryAfter.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	err := output.NewError(output.CodeRateLimited,
		"this token is sending requests faster than the API allows")
	err.Retryable = true
	return err.WithDetails(map[string]any{
		"retryAfterMs": ms,
		"limitPerSec":  limiter.rate,
		"burst":        int(limiter.burst),
	})
}

// rateLimitKey identifies one bucket: the token, not the socket. The local Unix
// identity has no token id, so all local callers share one bucket.
func rateLimitKey(identity Identity) string {
	if identity.TokenID != "" {
		return "token:" + identity.TokenID
	}
	return "local"
}
