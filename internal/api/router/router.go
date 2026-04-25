package router

import (
	"encoding/json"
	"net/http"

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
	mux         *http.ServeMux
	middlewares []func(http.Handler) http.Handler
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

// Handle registers a handler for a pattern
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
}

// HandleFunc registers a handler function for a pattern
func (r *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.mux.HandleFunc(pattern, handler)
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

// Group creates a new route group with additional middlewares
func (r *Router) Group(prefix string, middlewares ...func(http.Handler) http.Handler) *RouteGroup {
	return &RouteGroup{
		router:      r,
		prefix:      prefix,
		middlewares: middlewares,
	}
}

// RouteGroup represents a group of routes with common prefix and middlewares
type RouteGroup struct {
	router      *Router
	prefix      string
	middlewares []func(http.Handler) http.Handler
}

// Handle registers a handler for a pattern in the group
func (g *RouteGroup) Handle(pattern string, handler http.Handler) {
	// Apply group middlewares
	for i := len(g.middlewares) - 1; i >= 0; i-- {
		handler = g.middlewares[i](handler)
	}
	g.router.Handle(g.prefix+pattern, handler)
}

// HandleFunc registers a handler function for a pattern in the group
func (g *RouteGroup) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	g.Handle(pattern, http.HandlerFunc(handler))
}

// New creates and configures the main router
func New(cfg *config.Config, db *store.DB, logger *zap.Logger) http.Handler {
	router := NewRouter()

	// Initialize components
	jwtManager := auth.NewJWTManager(cfg.Security.JWT)
	hasher := crypto.NewArgon2Hasher(
		cfg.Security.Password.Argon2Time,
		cfg.Security.Password.Argon2Memory,
		cfg.Security.Password.Argon2Threads,
	)

	var keyRing *crypto.KeyRing
	if cfg.Database.EncryptionKey != "" {
		var err error
		keyRing, err = crypto.NewKeyRing(cfg.Database.EncryptionKey)
		if err != nil {
			logger.Warn("failed to initialize encryption key ring", zap.Error(err))
		}
	}

	// Initialize middlewares
	authMiddleware := middleware.NewAuthMiddleware(jwtManager)
	corsMiddleware := middleware.NewCORSMiddleware(cfg.Security.CORS)
	rateLimiter := middleware.NewRateLimiter(cfg.Security.RateLimit)
	loginLimiter := middleware.NewLoginRateLimiter(
		cfg.Security.RateLimit.LoginAttempts,
		cfg.Security.RateLimit.LoginWindow,
	)

	// Apply global middlewares (order matters: recovery should be outermost)
	router.Use(middleware.Recovery(logger))
	router.Use(middleware.SecurityHeaders)
	router.Use(middleware.RequestID)
	router.Use(middleware.LocaleMiddleware)
	router.Use(corsMiddleware.Handle)
	if cfg.Security.RateLimit.Enabled {
		router.Use(middleware.RateLimit(rateLimiter))
	}

	// Initialize password validator
	passwordValidator := password.NewValidator(cfg.Security.Password)

	// Initialize handlers (auditService is shared across all mutation handlers)
	auditService := handler.NewAuditService(db, logger)
	authHandler := handler.NewAuthHandler(db, jwtManager, hasher, passwordValidator, loginLimiter, auditService, logger)
	userHandler := handler.NewUserHandler(db, hasher, keyRing, passwordValidator, auditService, logger)
	serverHandler := handler.NewServerHandler(db, keyRing, auditService, logger)
	xmppHandler := handler.NewXMPPHandler(db, keyRing, auditService, logger)
	auditHandler := handler.NewAuditHandler(db, logger)

	// Health check (public)
	router.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Auth routes (public)
	router.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
	router.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)

	// Protected API routes
	api := router.Group("/api/v1", authMiddleware.Authenticate)

	// Auth (protected)
	api.HandleFunc("POST /auth/logout", authHandler.Logout)
	api.HandleFunc("GET /auth/me", authHandler.Me)
	api.HandleFunc("POST /auth/mfa/setup", authHandler.SetupMFA)
	api.HandleFunc("POST /auth/mfa/verify", authHandler.VerifyMFA)
	api.HandleFunc("POST /auth/mfa/disable", authHandler.DisableMFA)
	api.HandleFunc("POST /auth/password", authHandler.ChangePassword)

	// User management (admin only)
	adminGroup := router.Group("/api/v1",
		authMiddleware.Authenticate,
		middleware.RequireRole(models.RoleSuperAdmin, models.RoleAdmin),
	)
	adminGroup.HandleFunc("GET /users", userHandler.List)
	adminGroup.HandleFunc("POST /users", userHandler.Create)
	adminGroup.HandleFunc("GET /users/{id}", userHandler.Get)
	adminGroup.HandleFunc("PUT /users/{id}", userHandler.Update)
	adminGroup.HandleFunc("DELETE /users/{id}", userHandler.Delete)

	// Server management
	api.HandleFunc("GET /servers", serverHandler.List)
	api.Handle("POST /servers", middleware.RequirePermission("servers:write")(http.HandlerFunc(serverHandler.Create)))
	api.HandleFunc("GET /servers/{id}", serverHandler.Get)
	api.Handle("PUT /servers/{id}", middleware.RequirePermission("servers:write")(http.HandlerFunc(serverHandler.Update)))
	api.Handle("DELETE /servers/{id}", middleware.RequirePermission("servers:write")(http.HandlerFunc(serverHandler.Delete)))
	api.HandleFunc("GET /servers/{id}/stats", serverHandler.Stats)
	api.HandleFunc("POST /servers/{id}/test", serverHandler.Test)

	// XMPP operations
	api.HandleFunc("GET /servers/{serverId}/users", xmppHandler.ListUsers)
	api.HandleFunc("GET /servers/{serverId}/users/{username}", xmppHandler.GetUser)
	api.Handle("POST /servers/{serverId}/users", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.CreateUser)))
	api.Handle("DELETE /servers/{serverId}/users/{username}", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.DeleteUser)))
	api.Handle("POST /servers/{serverId}/users/{username}/kick", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.KickUser)))

	api.HandleFunc("GET /servers/{serverId}/sessions", xmppHandler.ListSessions)
	api.Handle("DELETE /servers/{serverId}/sessions/{jid}", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.KickSession)))

	api.HandleFunc("GET /servers/{serverId}/rooms", xmppHandler.ListRooms)
	api.HandleFunc("GET /servers/{serverId}/rooms/{room}", xmppHandler.GetRoom)
	api.Handle("POST /servers/{serverId}/rooms", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.CreateRoom)))
	api.Handle("DELETE /servers/{serverId}/rooms/{room}", middleware.RequirePermission("xmpp:write")(http.HandlerFunc(xmppHandler.DeleteRoom)))

	// Audit logs
	auditGroup := router.Group("/api/v1",
		authMiddleware.Authenticate,
		middleware.RequirePermission("audit:read"),
	)
	auditGroup.HandleFunc("GET /audit", auditHandler.List)
	auditGroup.HandleFunc("GET /audit/verify", auditHandler.Verify)
	auditGroup.HandleFunc("GET /audit/export", auditHandler.Export)

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
