package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/store/models"
)

type contextKey string

const (
	contextKeyUser      contextKey = "user"
	contextKeyClaims    contextKey = "claims"
	contextKeyRequestID contextKey = "request_id"
	contextKeyClientIP  contextKey = "client_ip"
)

// AuthMiddleware validates JWT tokens and adds user info to context
type AuthMiddleware struct {
	jwtManager *auth.JWTManager
}

// NewAuthMiddleware creates a new auth middleware
func NewAuthMiddleware(jwtManager *auth.JWTManager) *AuthMiddleware {
	return &AuthMiddleware{
		jwtManager: jwtManager,
	}
}

// Authenticate validates the JWT token from the Authorization header
func (m *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, "Invalid authorization header format", http.StatusUnauthorized)
			return
		}

		claims, err := m.jwtManager.ValidateToken(parts[1], auth.TokenTypeAccess)
		if err != nil {
			switch err {
			case auth.ErrExpiredToken:
				http.Error(w, "Token has expired", http.StatusUnauthorized)
			case auth.ErrInvalidToken, auth.ErrInvalidClaims:
				http.Error(w, "Invalid token", http.StatusUnauthorized)
			default:
				http.Error(w, "Authentication failed", http.StatusUnauthorized)
			}
			return
		}

		// Add claims to context
		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
	})
}

// RequirePermission checks if the authenticated user has the required permission
func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			userRole := models.Role(claims.Role)
			if !userRole.HasPermission(permission) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// WithClaims returns ctx carrying claims, as Authenticate does after a
// successful token check; tests use it to run handlers as a given user.
func WithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, contextKeyClaims, claims)
}

// GetClaims retrieves the JWT claims from the context
func GetClaims(ctx context.Context) *auth.Claims {
	claims, ok := ctx.Value(contextKeyClaims).(*auth.Claims)
	if !ok {
		return nil
	}
	return claims
}

// WithRequestID adds a request ID to the context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, contextKeyRequestID, requestID)
}

// GetRequestID retrieves the request ID from the context
func GetRequestID(ctx context.Context) string {
	id, ok := ctx.Value(contextKeyRequestID).(string)
	if !ok {
		return ""
	}
	return id
}

// CSRF middleware validates CSRF tokens for state-changing requests
type CSRFMiddleware struct {
	cookieName string
	headerName string
	secure     bool
}

// NewCSRFMiddleware creates a new CSRF middleware
func NewCSRFMiddleware(secure bool) *CSRFMiddleware {
	return &CSRFMiddleware{
		cookieName: "csrf_token",
		headerName: "X-CSRF-Token",
		secure:     secure,
	}
}

// Protect validates CSRF token for non-GET requests
func (m *CSRFMiddleware) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CSRF check for safe methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Get token from cookie
		cookie, err := r.Cookie(m.cookieName)
		if err != nil {
			http.Error(w, "CSRF token missing", http.StatusForbidden)
			return
		}

		// Get token from header
		headerToken := r.Header.Get(m.headerName)
		if headerToken == "" {
			http.Error(w, "CSRF token header missing", http.StatusForbidden)
			return
		}

		// Compare tokens
		if cookie.Value != headerToken {
			http.Error(w, "CSRF token mismatch", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
