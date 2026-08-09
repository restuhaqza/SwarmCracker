package main

import (
	"context"
	"net/http"
	"time"
)

// withRateLimit returns an HTTP middleware that applies token-bucket rate limiting.
// rate is the maximum requests per second, burst is the maximum burst size.
func withRateLimit(next http.Handler, rate, burst int) http.Handler {
	// Simple token bucket using buffered channel
	tokens := make(chan struct{}, burst)
	for i := 0; i < burst; i++ {
		tokens <- struct{}{}
	}

	// Refill tokens at given rate
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(rate))
		defer ticker.Stop()
		for range ticker.C {
			select {
			case tokens <- struct{}{}:
			default:
			}
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-tokens:
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		}
	})
}

// withRequestTimeout returns an HTTP middleware that applies a per-request timeout.
func withRequestTimeout(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
