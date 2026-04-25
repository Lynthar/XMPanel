package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCSRFMiddleware_AllowsSafeMethods(t *testing.T) {
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	// GET, HEAD, OPTIONS must pass through without checks.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/auth/refresh", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", method, rec.Code)
		}
	}
}

func TestCSRFMiddleware_RejectsMissingCookie(t *testing.T) {
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("X-CSRF-Token", "anything")

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("missing cookie: code = %d, want 403", rec.Code)
	}
}

func TestCSRFMiddleware_RejectsMissingHeader(t *testing.T) {
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "abc123"})

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("missing header: code = %d, want 403", rec.Code)
	}
}

func TestCSRFMiddleware_RejectsMismatch(t *testing.T) {
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "cookie-value"})
	req.Header.Set("X-CSRF-Token", "different-value")

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatch: code = %d, want 403", rec.Code)
	}
}

func TestCSRFMiddleware_AcceptsMatchingPair(t *testing.T) {
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("matching pair: code = %d, want 200", rec.Code)
	}
}

func TestCSRFMiddleware_EmptyHeaderEvenWithCookie(t *testing.T) {
	// Defense against attackers setting only the cookie via a victim's browser
	// while omitting the header. Header presence is mandatory.
	m := NewCSRFMiddleware(false)
	h := m.Protect(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: ""})
	req.Header.Set("X-CSRF-Token", "")

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("empty pair: code = %d, want 403", rec.Code)
	}
}
