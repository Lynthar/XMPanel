package router

import (
	"net/http"
	"strings"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/handler"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/security/password"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// Router wraps http.ServeMux with middleware support
type Router struct {
	adapters       *registry.Registry
	mux            *http.ServeMux
	middlewares    []func(http.Handler) http.Handler
	endpoints      []endpoint
	authMiddleware *middleware.AuthMiddleware
	csrfMiddleware *middleware.CSRFMiddleware
}

// NewRouter creates a new router
func NewRouter() *Router {
	return &Router{
		mux:         http.NewServeMux(),
		middlewares: make([]func(http.Handler) http.Handler, 0),
	}
}

// Use adds a middleware to the router
func (r *Router) Use(mw func(http.Handler) http.Handler) {
	r.middlewares = append(r.middlewares, mw)
}

// Handle registers an allowlisted public route.
func (r *Router) Handle(pattern string, handler http.Handler) {
	if !publicRoutes[pattern] {
		panic("public route is not allowlisted: " + pattern)
	}
	r.mux.Handle(pattern, handler)
	method, path, hasMethod := strings.Cut(pattern, " ")
	if !hasMethod {
		method, path = "", pattern
	}
	r.endpoints = append(r.endpoints, endpoint{method: method, path: path})
}

// HandleFunc registers a handler function for a pattern
func (r *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(handler))
}

// ServeHTTP implements http.Handler
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Apply middlewares in reverse order
	var handler http.Handler = r.mux
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		handler = r.middlewares[i](handler)
	}
	handler.ServeHTTP(w, req)
}

const permissionSelf = "auth:self"

var publicRoutes = map[string]bool{
	"GET /health":               true,
	"POST /api/v1/auth/login":   true,
	"POST /api/v1/auth/refresh": true,
	"/":                         true,
}

type endpoint struct {
	method, path, permission string
}

// route requires an explicit permission for every authenticated endpoint.
// auth:self is limited to auth routes whose target comes from JWT claims;
// other routes must use a permission declared by the role table.
func (r *Router) route(method, path, permission string, h http.HandlerFunc) {
	known := false
	if permission == permissionSelf {
		known = strings.HasPrefix(path, "/api/v1/auth/")
	} else if permission != "" {
		for _, permissions := range models.Permissions {
			for _, value := range permissions {
				if permission == value {
					known = true
				}
			}
		}
	}
	if !known {
		panic("route requires a known permission: " + method + " " + path)
	}
	var protected http.Handler = h
	if permission != permissionSelf {
		protected = middleware.RequirePermission(permission)(protected)
	}
	protected = r.csrfMiddleware.Protect(protected)
	protected = r.authMiddleware.Authenticate(protected)
	r.mux.Handle(method+" "+path, protected)
	r.endpoints = append(r.endpoints, endpoint{method, path, permission})
}

// New creates and configures the main router
func New(cfg *config.Config, db *store.DB, keyRing *crypto.KeyRing, logger *zap.Logger) *Router {
	router := NewRouter()

	// Initialize components
	jwtManager := auth.NewJWTManager(cfg.Security.JWT)
	hasher := crypto.NewArgon2Hasher(
		cfg.Security.Password.Argon2Time,
		cfg.Security.Password.Argon2Memory,
		cfg.Security.Password.Argon2Threads,
	)

	// Initialize middlewares
	authMiddleware := middleware.NewAuthMiddleware(jwtManager)
	corsMiddleware := middleware.NewCORSMiddleware(cfg.Security.CORS)
	clientIPResolver := middleware.NewClientIPResolver(
		cfg.Security.RateLimit.TrustXForwardedFor,
		cfg.Security.RateLimit.TrustedProxies,
	)
	rateLimiter := middleware.NewRateLimiter(cfg.Security.RateLimit)
	loginLimiter := middleware.NewLoginRateLimiter(
		cfg.Security.RateLimit.LoginAttempts,
		cfg.Security.RateLimit.LoginWindow,
	)

	// Apply global middlewares (order matters: recovery should be outermost,
	// and ClientIP has to precede RateLimit and every handler recording an IP)
	router.Use(middleware.Recovery(logger))
	router.Use(middleware.SecurityHeaders)
	router.Use(middleware.ClientIP(clientIPResolver))
	router.Use(middleware.RequestID)
	router.Use(middleware.LocaleMiddleware)
	router.Use(corsMiddleware.Handle)
	if cfg.Security.RateLimit.Enabled {
		router.Use(middleware.RateLimit(rateLimiter))
	}

	// Initialize password validator
	passwordValidator := password.NewValidator(cfg.Security.Password)

	router.adapters = registry.New(db, keyRing, logger)

	// Initialize handlers (auditService is shared across all mutation handlers)
	auditService := handler.NewAuditService(db, logger)
	authHandler := handler.NewAuthHandler(
		db, jwtManager, hasher, passwordValidator, loginLimiter, auditService,
		cfg.Security.JWT.RefreshTokenTTL, cfg.CookieSecure(), logger,
	)
	userHandler := handler.NewUserHandler(db, hasher, keyRing, passwordValidator, auditService, logger)
	serverHandler := handler.NewServerHandler(db, keyRing, router.adapters, auditService, logger)
	backendHandler := handler.NewBackendHandler(router.adapters, auditService, logger)
	matrixHandler := handler.NewMatrixHandler(router.adapters, auditService, logger)
	auditHandler := handler.NewAuditHandler(db, logger)
	csrfMiddleware := middleware.NewCSRFMiddleware(cfg.CookieSecure())
	router.authMiddleware = authMiddleware
	router.csrfMiddleware = csrfMiddleware

	// Health check (public). Aggregate-only response shape — see health.go for
	// the contract and disclosure rationale.
	router.HandleFunc("GET /health", newHealthHandler(db, router.adapters, logger))

	// Auth routes (public). Login has no CSRF — the user has no session yet
	// so there's no cookie to mirror; SameSite=Strict on the cookies set by a
	// successful login is what defends against login CSRF.
	router.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
	// /auth/refresh relies on the xmpanel_refresh HttpOnly cookie which the
	// browser auto-attaches; CSRF middleware enforces double-submit so a
	// cross-origin form can't ride the cookie.
	router.Handle("POST /api/v1/auth/refresh",
		csrfMiddleware.Protect(http.HandlerFunc(authHandler.Refresh)))

	// Authenticated routes always apply authentication and CSRF before the
	// declared permission. CSRF leaves safe methods unchanged.

	// Auth (protected)
	router.route("POST", "/api/v1/auth/logout", permissionSelf, authHandler.Logout)
	router.route("GET", "/api/v1/auth/me", permissionSelf, authHandler.Me)
	router.route("POST", "/api/v1/auth/mfa/setup", permissionSelf, authHandler.SetupMFA)
	router.route("POST", "/api/v1/auth/mfa/verify", permissionSelf, authHandler.VerifyMFA)
	router.route("POST", "/api/v1/auth/mfa/disable", permissionSelf, authHandler.DisableMFA)
	router.route("POST", "/api/v1/auth/password", permissionSelf, authHandler.ChangePassword)

	// User management permissions are held by admin and superadmin.
	router.route("GET", "/api/v1/users", "users:read", userHandler.List)
	router.route("POST", "/api/v1/users", "users:write", userHandler.Create)
	router.route("GET", "/api/v1/users/{id}", "users:read", userHandler.Get)
	router.route("PUT", "/api/v1/users/{id}", "users:write", userHandler.Update)
	router.route("DELETE", "/api/v1/users/{id}", "users:write", userHandler.Delete)

	// Server management
	router.route("GET", "/api/v1/servers", "servers:read", serverHandler.List)
	router.route("POST", "/api/v1/servers", "servers:write", serverHandler.Create)
	router.route("GET", "/api/v1/servers/{id}", "servers:read", serverHandler.Get)
	router.route("PUT", "/api/v1/servers/{id}", "servers:write", serverHandler.Update)
	router.route("DELETE", "/api/v1/servers/{id}", "servers:write", serverHandler.Delete)
	router.route("GET", "/api/v1/servers/{id}/stats", "servers:read", serverHandler.Stats)
	router.route("GET", "/api/v1/servers/{id}/capabilities", "servers:read", serverHandler.Capabilities)
	router.route("POST", "/api/v1/servers/{id}/test", "servers:read", serverHandler.Test)

	// Backend objects: accounts, sessions and rooms behind a registered server.
	// Account and session ids are URL-encoded JIDs or MXIDs in one path segment.
	router.route("GET", "/api/v1/servers/{serverId}/accounts", "backend:read", backendHandler.ListAccounts)
	router.route("POST", "/api/v1/servers/{serverId}/accounts", "backend:write", backendHandler.CreateAccount)
	router.route("GET", "/api/v1/servers/{serverId}/accounts/{account}", "backend:read", backendHandler.GetAccount)
	router.route("DELETE", "/api/v1/servers/{serverId}/accounts/{account}", "backend:write", backendHandler.DeleteAccount)
	router.route("PUT", "/api/v1/servers/{serverId}/accounts/{account}/password", "backend:write", backendHandler.SetPassword)
	router.route("PUT", "/api/v1/servers/{serverId}/accounts/{account}/enabled", "backend:write", backendHandler.SetEnabled)
	router.route("PUT", "/api/v1/servers/{serverId}/accounts/{account}/admin", "backend:write", backendHandler.SetAdmin)
	router.route("GET", "/api/v1/servers/{serverId}/accounts/{account}/sessions", "backend:read", backendHandler.ListAccountSessions)
	router.route("DELETE", "/api/v1/servers/{serverId}/accounts/{account}/sessions", "backend:write", backendHandler.TerminateAccountSessions)

	router.route("GET", "/api/v1/servers/{serverId}/sessions", "backend:read", backendHandler.ListSessions)
	router.route("DELETE", "/api/v1/servers/{serverId}/sessions/{session}", "backend:write", backendHandler.TerminateSession)

	router.route("GET", "/api/v1/servers/{serverId}/rooms", "backend:read", backendHandler.ListRooms)
	router.route("POST", "/api/v1/servers/{serverId}/rooms", "backend:write", backendHandler.CreateRoom)
	router.route("GET", "/api/v1/servers/{serverId}/rooms/{room}", "backend:read", backendHandler.GetRoom)
	router.route("DELETE", "/api/v1/servers/{serverId}/rooms/{room}", "backend:write", backendHandler.DeleteRoom)

	// Matrix moderation, served only by adapters implementing MatrixAdmin
	// (others answer 501). Erasure, shadow ban, room block and purge with
	// options need backend:danger; the plain room delete stays a write.
	router.route("POST", "/api/v1/servers/{serverId}/matrix/accounts/{account}/deactivate", "backend:danger", matrixHandler.Deactivate)
	router.route("PUT", "/api/v1/servers/{serverId}/matrix/accounts/{account}/suspended", "backend:write", matrixHandler.SetSuspended)
	router.route("PUT", "/api/v1/servers/{serverId}/matrix/accounts/{account}/shadow-banned", "backend:danger", matrixHandler.SetShadowBanned)
	router.route("GET", "/api/v1/servers/{serverId}/matrix/accounts/{account}/media", "backend:read", matrixHandler.ListAccountMedia)
	router.route("POST", "/api/v1/servers/{serverId}/matrix/accounts/{account}/media/quarantine", "backend:write", matrixHandler.QuarantineAccountMedia)
	router.route("DELETE", "/api/v1/servers/{serverId}/matrix/media/{mediaId}", "backend:write", matrixHandler.DeleteMedia)
	// Listing is a write permission: a registration token opens accounts
	// upstream, so a read-only role holding one would hold a write power the
	// panel never audits.
	router.route("GET", "/api/v1/servers/{serverId}/matrix/registration-tokens", "backend:write", matrixHandler.ListRegistrationTokens)
	router.route("POST", "/api/v1/servers/{serverId}/matrix/registration-tokens", "backend:write", matrixHandler.CreateRegistrationToken)
	router.route("DELETE", "/api/v1/servers/{serverId}/matrix/registration-tokens/{token}", "backend:write", matrixHandler.DeleteRegistrationToken)
	router.route("GET", "/api/v1/servers/{serverId}/matrix/reports", "backend:read", matrixHandler.ListReports)
	router.route("POST", "/api/v1/servers/{serverId}/matrix/rooms/{room}/block", "backend:danger", matrixHandler.BlockRoom)
	router.route("POST", "/api/v1/servers/{serverId}/matrix/rooms/{room}/purge", "backend:danger", matrixHandler.PurgeRoom)
	router.route("POST", "/api/v1/servers/{serverId}/matrix/notices", "backend:write", matrixHandler.SendNotice)
	router.route("GET", "/api/v1/servers/{serverId}/matrix/federation", "backend:read", matrixHandler.ListFederation)

	// Audit logs require audit:read.
	router.route("GET", "/api/v1/audit", "audit:read", auditHandler.List)
	router.route("GET", "/api/v1/audit/verify", "audit:read", auditHandler.Verify)
	router.route("GET", "/api/v1/audit/export", "audit:read", auditHandler.Export)

	// Serve static files (frontend) for non-API routes
	fs := http.FileServer(http.Dir("web/dist"))
	router.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA routing: serve index.html for non-file requests
		if r.URL.Path != "/" && !hasFileExtension(r.URL.Path) {
			http.ServeFile(w, r, "web/dist/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	}))

	return router
}

func hasFileExtension(path string) bool {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return true
		}
		if path[i] == '/' {
			return false
		}
	}
	return false
}

// Close releases cached adapter connections after HTTP requests have drained.
func (r *Router) Close() {
	if r.adapters != nil {
		r.adapters.Close()
	}
}
