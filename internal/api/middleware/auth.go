package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/store/models"
)

type contextKey string

const (
	contextKeyUser      contextKey = "user"
	contextKeyClaims    contextKey = "claims"
	contextKeyRequestID contextKey = "request_id"
	contextKeyClientIP  contextKey = "client_ip"
)

// ErrSessionGone is what a SessionLookup returns when the account or the
// session a token was issued for no longer exists.
var ErrSessionGone = errors.New("session no longer exists")

// SessionLookup returns the current role of userID while sessionID is still
// one of its sessions, and ErrSessionGone otherwise.
type SessionLookup func(ctx context.Context, userID int64, sessionID string) (string, error)

// AuthMiddleware validates JWT tokens and adds user info to context
type AuthMiddleware struct {
	jwtManager *auth.JWTManager
	lookup     SessionLookup
}

// NewAuthMiddleware creates a new auth middleware. lookup is consulted on
// every request: a token alone would keep a deleted, demoted or logged-out
// user's old rights until it expires.
func NewAuthMiddleware(jwtManager *auth.JWTManager, lookup SessionLookup) *AuthMiddleware {
	return &AuthMiddleware{
		jwtManager: jwtManager,
		lookup:     lookup,
	}
}

// Authenticate validates the JWT token from the Authorization header and
// replaces its role with the account's current one.
func (m *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenInvalid)
			return
		}

		claims, err := m.jwtManager.ValidateToken(parts[1], auth.TokenTypeAccess)
		if err != nil {
			if errors.Is(err, auth.ErrExpiredToken) {
				writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenExpired)
			} else {
				writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenInvalid)
			}
			return
		}

		role, err := m.lookup(r.Context(), claims.UserID, claims.SessionID)
		if errors.Is(err, ErrSessionGone) {
			writeError(w, r, http.StatusUnauthorized, i18n.MsgSessionRevoked)
			return
		}
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, i18n.MsgInternalError)
			return
		}
		current := *claims
		current.Role = role

		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), &current)))
	})
}

// RequirePermission checks if the authenticated user has the required permission
func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
				return
			}

			userRole := models.Role(claims.Role)
			if !userRole.HasPermission(permission) {
				writeError(w, r, http.StatusForbidden, i18n.MsgForbidden)
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
			writeError(w, r, http.StatusForbidden, i18n.MsgCSRFRejected)
			return
		}

		// Get token from header
		headerToken := r.Header.Get(m.headerName)
		if headerToken == "" {
			writeError(w, r, http.StatusForbidden, i18n.MsgCSRFRejected)
			return
		}

		// Compare tokens
		if cookie.Value != headerToken {
			writeError(w, r, http.StatusForbidden, i18n.MsgCSRFRejected)
			return
		}

		next.ServeHTTP(w, r)
	})
}
