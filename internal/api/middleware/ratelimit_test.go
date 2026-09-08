package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xmpanel/xmpanel/internal/config"
)

// probeClientIP runs req through the ClientIP middleware and returns what
// GetClientIP reports downstream — the value audit rows and the login lockout
// actually store.
func probeClientIP(res *ClientIPResolver, req *http.Request) string {
	var seen string
	h := ClientIP(res)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetClientIP(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestClientIP_IgnoresHeadersFromUntrustedPeer(t *testing.T) {
	res := NewClientIPResolver(true, []string{"10.0.0.1"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:51234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "5.6.7.8")

	if got := probeClientIP(res, req); got != "203.0.113.9" {
		t.Errorf("got %q, want the peer address 203.0.113.9", got)
	}
}

func TestClientIP_TrustsForwardedForFromTrustedProxy(t *testing.T) {
	res := NewClientIPResolver(true, []string{"127.0.0.1", "::1"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 127.0.0.1")

	if got := probeClientIP(res, req); got != "198.51.100.7" {
		t.Errorf("got %q, want 198.51.100.7", got)
	}
}

func TestClientIP_FallsBackToRealIP(t *testing.T) {
	res := NewClientIPResolver(true, []string{"127.0.0.1"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Real-IP", "198.51.100.8")

	if got := probeClientIP(res, req); got != "198.51.100.8" {
		t.Errorf("got %q, want 198.51.100.8", got)
	}
}

// The flag alone is not enough: with no proxy listed there is nobody to
// believe, so a direct client cannot forge its address by setting the header.
func TestClientIP_TrustFlagWithoutProxyListIgnoresHeaders(t *testing.T) {
	res := NewClientIPResolver(true, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := probeClientIP(res, req); got != "127.0.0.1" {
		t.Errorf("got %q, want 127.0.0.1", got)
	}
}

func TestClientIP_DisabledTrustIgnoresHeaders(t *testing.T) {
	res := NewClientIPResolver(false, []string{"127.0.0.1"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := probeClientIP(res, req); got != "127.0.0.1" {
		t.Errorf("got %q, want 127.0.0.1", got)
	}
}

func TestClientIP_StripsPort(t *testing.T) {
	res := NewClientIPResolver(false, nil)

	for addr, want := range map[string]string{
		"192.0.2.5:12345":   "192.0.2.5",
		"[2001:db8::1]:443": "2001:db8::1",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr
		if got := probeClientIP(res, req); got != want {
			t.Errorf("%s: got %q, want %q", addr, got, want)
		}
	}
}

// Without the middleware GetClientIP must still refuse to believe headers,
// so a handler reached off the main chain cannot be fed a forged address.
func TestGetClientIP_WithoutMiddlewareIgnoresHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.5:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := GetClientIP(req); got != "192.0.2.5" {
		t.Errorf("got %q, want 192.0.2.5", got)
	}
}

// RateLimit keys on the resolved address; two clients behind one trusted
// proxy must not share a bucket.
func TestRateLimit_KeysOnResolvedClientIP(t *testing.T) {
	res := NewClientIPResolver(true, []string{"127.0.0.1"})
	limiter := NewRateLimiter(config.RateLimitConfig{RequestsPerSecond: 0, Burst: 1})

	chain := ClientIP(res)(RateLimit(limiter)(okHandler()))

	call := func(forwarded string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("X-Forwarded-For", forwarded)
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first client: code = %d, want 200", code)
	}
	if code := call("198.51.100.2"); code != http.StatusOK {
		t.Errorf("second client: code = %d, want 200 (it has its own bucket)", code)
	}
	if code := call("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Errorf("first client again: code = %d, want 429", code)
	}
}
