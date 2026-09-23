package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/config"
)

// RateLimiter implements a token bucket rate limiter
type RateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rate      float64
	burst     int
	cleanup   time.Duration
	lastClean time.Time
}

type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(cfg config.RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		buckets:   make(map[string]*bucket),
		rate:      cfg.RequestsPerSecond,
		burst:     cfg.Burst,
		cleanup:   5 * time.Minute,
		lastClean: time.Now(),
	}
}

// Allow checks if a request from the given key should be allowed
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Periodic cleanup of old buckets
	if time.Since(rl.lastClean) > rl.cleanup {
		rl.cleanupBuckets()
		rl.lastClean = time.Now()
	}

	now := time.Now()
	b, exists := rl.buckets[key]

	if !exists {
		rl.buckets[key] = &bucket{
			tokens:    float64(rl.burst) - 1,
			lastCheck: now,
		}
		return true
	}

	// Add tokens based on time elapsed
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastCheck = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

func (rl *RateLimiter) cleanupBuckets() {
	threshold := time.Now().Add(-10 * time.Minute)
	for key, b := range rl.buckets {
		if b.lastCheck.Before(threshold) {
			delete(rl.buckets, key)
		}
	}
}

// RateLimit middleware limits requests based on client IP. Register it after
// ClientIP, or every request behind a proxy shares one bucket.
func RateLimit(limiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := GetClientIP(r)

			if !limiter.Allow(key) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// LoginRateLimiter specifically limits login attempts
type LoginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempts
	maxTries int
	window   time.Duration
}

type loginAttempts struct {
	count       int
	firstTry    time.Time
	lockedUntil time.Time
}

// NewLoginRateLimiter creates a new login rate limiter
func NewLoginRateLimiter(maxTries int, window time.Duration) *LoginRateLimiter {
	return &LoginRateLimiter{
		attempts: make(map[string]*loginAttempts),
		maxTries: maxTries,
		window:   window,
	}
}

// Check checks if a login attempt should be allowed
func (lr *LoginRateLimiter) Check(key string) (bool, time.Duration) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	now := time.Now()
	a, exists := lr.attempts[key]

	if !exists {
		lr.attempts[key] = &loginAttempts{
			count:    1,
			firstTry: now,
		}
		return true, 0
	}

	// Check if locked
	if !a.lockedUntil.IsZero() && now.Before(a.lockedUntil) {
		return false, a.lockedUntil.Sub(now)
	}

	// Check if window has passed
	if now.Sub(a.firstTry) > lr.window {
		a.count = 1
		a.firstTry = now
		a.lockedUntil = time.Time{}
		return true, 0
	}

	a.count++

	if a.count > lr.maxTries {
		// Progressive lockout: double the lockout time for each subsequent lockout
		lockoutDuration := lr.window
		a.lockedUntil = now.Add(lockoutDuration)
		return false, lockoutDuration
	}

	return true, 0
}

// RecordFailure records a failed login attempt
func (lr *LoginRateLimiter) RecordFailure(key string) {
	// The Check method already records the attempt
}

// RecordSuccess clears the attempts for a key after successful login
func (lr *LoginRateLimiter) RecordSuccess(key string) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	delete(lr.attempts, key)
}

// ClientIPResolver decides which address counts as the caller's. It is the
// only place proxy headers are believed: rate limiting, login lockout, the
// sessions and users IP columns and every audit row all read its answer, so
// two implementations would mean the audit log and the limiter disagree about
// who did something.
type ClientIPResolver struct {
	trustedProxies     []*net.IPNet
	trustXForwardedFor bool
}

// NewClientIPResolver builds a resolver from the trusted-proxy settings.
// Unparseable entries are dropped; an empty list means no proxy is trusted,
// so the headers are ignored however trustXForwardedFor is set.
func NewClientIPResolver(trustXForwardedFor bool, trustedProxies []string) *ClientIPResolver {
	res := &ClientIPResolver{trustXForwardedFor: trustXForwardedFor}

	for _, proxy := range trustedProxies {
		// Handle single IPs by adding /32 or /128
		if !strings.Contains(proxy, "/") {
			if strings.Contains(proxy, ":") {
				proxy += "/128"
			} else {
				proxy += "/32"
			}
		}
		_, network, err := net.ParseCIDR(proxy)
		if err == nil {
			res.trustedProxies = append(res.trustedProxies, network)
		}
	}

	return res
}

// Resolve returns the caller's IP, consulting X-Forwarded-For / X-Real-IP
// only when the connection itself comes from a trusted proxy. Anyone can set
// those headers, so trusting them unconditionally lets a client pick its own
// rate-limit bucket and its own audit trail.
//
// X-Forwarded-For is walked from the right, past every listed proxy, to the
// first address no listed proxy vouches for; everything left of that the
// client could have written itself. Every proxy in front of the panel must
// therefore be in trustedProxies, or the address of the outermost unlisted
// one is taken for the client. X-Real-IP is read only without
// X-Forwarded-For, and is only as good as the proxy that overwrites it.
func (res *ClientIPResolver) Resolve(r *http.Request) string {
	remoteIP := extractIP(r.RemoteAddr)

	if !res.trustXForwardedFor || !res.isTrustedProxy(remoteIP) {
		return remoteIP
	}

	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		return res.firstUntrustedHop(remoteIP, strings.Split(strings.Join(xff, ","), ","))
	}

	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}

	return remoteIP
}

// firstUntrustedHop walks hops from the right, as each proxy appended them.
// An entry that is not an address ends the walk at the hop that reported it.
func (res *ClientIPResolver) firstUntrustedHop(remoteIP string, hops []string) string {
	client := remoteIP
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break
		}
		client = ip.String()
		if !res.isTrustedProxy(client) {
			break
		}
	}
	return client
}

// isTrustedProxy checks if the given IP is in the trusted proxies list
func (res *ClientIPResolver) isTrustedProxy(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	for _, network := range res.trustedProxies {
		if network.Contains(parsedIP) {
			return true
		}
	}

	return false
}

// ClientIP resolves the caller's address once per request and stores it for
// GetClientIP. Register it ahead of RateLimit and of every handler that
// records an IP.
func ClientIP(res *ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), contextKeyClientIP, res.Resolve(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractIP extracts the IP address from an address string (removes port)
func extractIP(addr string) string {
	// Handle IPv6 addresses in brackets
	if strings.HasPrefix(addr, "[") {
		if idx := strings.Index(addr, "]"); idx != -1 {
			return addr[1:idx]
		}
	}

	// Handle host:port format
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}

	return addr
}

// GetClientIP returns the address the ClientIP middleware resolved for this
// request. Without that middleware it falls back to the peer address, which
// trusts no header — the safe answer, never a spoofable one.
func GetClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(contextKeyClientIP).(string); ok {
		return ip
	}
	return extractIP(r.RemoteAddr)
}
