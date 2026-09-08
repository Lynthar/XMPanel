package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// SecurityHeaders adds security headers to responses
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")

		// XSS protection
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Content Security Policy
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none';")

		// Permissions policy
		w.Header().Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()")

		// Remove server identification
		w.Header().Del("Server")
		w.Header().Del("X-Powered-By")

		// HSTS (only enable if using HTTPS)
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}

		next.ServeHTTP(w, r)
	})
}

// HideServerInfo removes or masks server identification headers
func HideServerInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a custom ResponseWriter to intercept and remove headers
		wrapped := &headerFilterWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
	})
}

type headerFilterWriter struct {
	http.ResponseWriter
}

func (w *headerFilterWriter) WriteHeader(statusCode int) {
	// Remove identifying headers before writing
	w.Header().Del("Server")
	w.Header().Del("X-Powered-By")
	w.ResponseWriter.WriteHeader(statusCode)
}

// maxClientRequestIDLen bounds a caller-supplied X-Request-ID. audit_logs
// stores it in a VARCHAR(255); a longer value makes the audit INSERT fail,
// and a failed audit write leaves a hole in the hash chain.
const maxClientRequestIDLen = 64

// RequestID generates and adds a unique request ID to each request.
//
// A client-supplied X-Request-ID is echoed only when it is short and made of
// [A-Za-z0-9._-]; anything else is replaced by a generated id. The header
// reaches storage, so it is untrusted input, not a trace hint to pass through.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = generateRequestID()
		}

		// Add to response headers
		w.Header().Set("X-Request-ID", requestID)

		// Add to request context
		ctx := WithRequestID(r.Context(), requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sanitizeRequestID returns id when it is safe to store and echo, else "".
func sanitizeRequestID(id string) string {
	if id == "" || len(id) > maxClientRequestIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	return id
}

// generateRequestID generates a cryptographically random request ID
func generateRequestID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// crypto/rand failure is extremely rare and indicates serious system issues
		// In this case, we panic as the system is in an unsafe state
		panic("crypto/rand: failed to generate random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}
