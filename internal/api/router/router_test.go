package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

func testRouter(t *testing.T) (*Router, *auth.JWTManager) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Security.JWT.Secret = strings.Repeat("route-test", 4)
	cfg.Security.JWT.AccessTokenTTL = time.Hour
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, nil, ring, zap.NewNop())
	t.Cleanup(r.Close)
	return r, auth.NewJWTManager(cfg.Security.JWT)
}

func allowedEndpoint(role models.Role, e endpoint) bool {
	if role == models.RoleSuperAdmin || role == models.RoleAdmin {
		return true
	}
	if strings.HasPrefix(e.path, "/api/v1/auth/") {
		return true
	}
	if strings.HasPrefix(e.path, "/api/v1/users") {
		return false
	}
	if strings.HasPrefix(e.path, "/api/v1/audit") {
		return role == models.RoleAuditor
	}
	if strings.Contains(e.path, "{serverId}") {
		if e.permission == "backend:danger" {
			return false
		}
		if e.permission == "backend:write" {
			return role == models.RoleOperator
		}
		return role == models.RoleOperator || (role == models.RoleViewer && e.method == http.MethodGet)
	}
	return e.method == http.MethodGet || strings.HasSuffix(e.path, "/test")
}

// Every role's reach over the backend routes, spelled out so a permission
// table change has to be mirrored here on purpose.
func TestBackendPermissionsPerRole(t *testing.T) {
	for role, want := range map[models.Role][]string{
		models.RoleAdmin:    {"backend:read", "backend:write", "backend:danger"},
		models.RoleOperator: {"backend:read", "backend:write"},
		models.RoleViewer:   {"backend:read"},
		models.RoleAuditor:  {},
	} {
		for _, permission := range []string{"backend:read", "backend:write", "backend:danger"} {
			has := role.HasPermission(permission)
			expected := false
			for _, w := range want {
				expected = expected || w == permission
			}
			if has != expected {
				t.Errorf("%s has %s = %v, want %v", role, permission, has, expected)
			}
		}
	}
}

func TestAuthenticatedRouteAccess(t *testing.T) {
	application, tokens := testRouter(t)
	for _, e := range application.endpoints {
		if e.permission == "" {
			pattern := e.method + " " + e.path
			if e.method == "" {
				pattern = e.path
			}
			if !publicRoutes[pattern] {
				t.Errorf("unprotected route: %s", pattern)
			}
			continue
		}
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			r := NewRouter()
			r.authMiddleware = application.authMiddleware
			r.csrfMiddleware = middleware.NewCSRFMiddleware(false)
			reached := false
			r.route(e.method, e.path, e.permission, func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			})
			path := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(e.path, "1")
			for _, role := range []models.Role{"", models.RoleSuperAdmin, models.RoleAdmin, models.RoleOperator, models.RoleViewer, models.RoleAuditor} {
				req := httptest.NewRequest(e.method, path, nil)
				if role != "" {
					pair, err := tokens.GenerateTokenPair(1, "tester", string(role), "session", "")
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
				}
				req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "csrf-test"})
				req.Header.Set("X-CSRF-Token", "csrf-test")
				reached = false
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)
				want := http.StatusForbidden
				if role == "" {
					want = http.StatusUnauthorized
				} else if allowedEndpoint(role, e) {
					want = http.StatusNoContent
				}
				if rec.Code != want || reached != (want == http.StatusNoContent) {
					t.Errorf("role %q: status %d, reached %v; want %d", role, rec.Code, reached, want)
				}
				if want == http.StatusForbidden && rec.Body.String() != "Forbidden\n" {
					t.Errorf("permission response changed: %q", rec.Body.String())
				}
				if role == models.RoleAdmin && e.method != http.MethodGet {
					req.Header.Del("Cookie")
					reached = false
					rec = httptest.NewRecorder()
					r.ServeHTTP(rec, req)
					if rec.Code != http.StatusForbidden || reached || rec.Body.String() != "CSRF token missing\n" {
						t.Errorf("unsafe route omitted CSRF: %d %q", rec.Code, rec.Body.String())
					}
				}
			}
		})
	}
}

func TestRoutesRejectMissingOrUnknownAccess(t *testing.T) {
	for _, permission := range []string{"", "missing:permission", permissionSelf} {
		t.Run(permission, func(t *testing.T) {
			r, _ := testRouter(t)
			defer func() {
				if recover() == nil {
					t.Fatal("registration accepted missing or invalid permission")
				}
			}()
			r.route(http.MethodGet, "/api/v1/new-backend", permission, func(http.ResponseWriter, *http.Request) {})
		})
	}
	t.Run("public route", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("non-allowlisted public registration succeeded")
			}
		}()
		NewRouter().HandleFunc("GET /api/v1/new-backend", func(http.ResponseWriter, *http.Request) {})
	})
}

func TestFrontendAPIMatchesRoutes(t *testing.T) {
	r, _ := testRouter(t)
	source, err := os.ReadFile("../../../web/src/lib/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	variables := regexp.MustCompile(`\{[^}]+\}`)
	calls := regexp.MustCompile(`api\.(get|post|put|delete)\(['\x60]([^'\x60]+)['\x60]`)
	frontend := make(map[string]bool)
	for _, match := range calls.FindAllStringSubmatch(string(source), -1) {
		path := variables.ReplaceAllString(strings.ReplaceAll(match[2], "$", ""), "{}")
		frontend[strings.ToUpper(match[1])+" /api/v1"+path] = true
	}
	if len(frontend) == 0 {
		t.Fatal("no frontend calls found")
	}
	for _, e := range r.endpoints {
		if !strings.HasPrefix(e.path, "/api/v1/") {
			continue
		}
		key := e.method + " " + variables.ReplaceAllString(e.path, "{}")
		if !frontend[key] {
			t.Errorf("route has no frontend API: %s", key)
		}
		delete(frontend, key)
	}
	for key := range frontend {
		t.Errorf("frontend API has no route: %s", key)
	}
}

func TestSessionRoutePreservesEscapedJID(t *testing.T) {
	application, tokens := testRouter(t)
	r := NewRouter()
	r.authMiddleware = application.authMiddleware
	r.csrfMiddleware = application.csrfMiddleware
	const jid = "alice@example.com/phone/one"
	r.route("DELETE", "/api/v1/servers/{serverId}/sessions/{session}", "backend:write", func(w http.ResponseWriter, req *http.Request) {
		if req.PathValue("session") != jid {
			t.Errorf("session = %q", req.PathValue("session"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	pair, err := tokens.GenerateTokenPair(1, "tester", "operator", "session", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("DELETE", "/api/v1/servers/1/sessions/"+url.PathEscape(jid), nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-CSRF-Token", "test")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "test"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}
