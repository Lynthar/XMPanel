package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/prosody/prosodytest"
	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/store/models"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

type backendFixture struct {
	fake     *prosodytest.Fake
	upstream *httptest.Server
	adapters *registry.Registry
	backend  *BackendHandler
	servers  *ServerHandler
	audit    *AuditService
	id       string
	serverID int64
	userID   int64
}

func newBackendFixture(t *testing.T) *backendFixture {
	t.Helper()
	db := newTestDB(t)
	ring := storetest.NewKeyRing(t)
	f := &backendFixture{fake: prosodytest.NewFake()}
	f.fake.Reset()
	f.upstream = httptest.NewServer(f.fake)
	t.Cleanup(f.upstream.Close)
	f.serverID = storetest.InsertServer(t, db, ring, "prosody", f.upstream.URL, prosodytest.Domain, prosodytest.Token)
	f.id = strconv.FormatInt(f.serverID, 10)
	// audit_logs.user_id references users, so the acting admin must exist.
	err := db.QueryRow(`INSERT INTO users (username, email, password_hash, role) VALUES ('backend-tester', 'backend-tester@localhost', 'x', 'admin')
		ON CONFLICT (username) DO UPDATE SET role = 'admin' RETURNING id`).Scan(&f.userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, f.userID) })
	logger := zap.NewNop()
	f.adapters = registry.New(db, ring, logger)
	t.Cleanup(f.adapters.Close)
	f.audit = NewAuditService(db, logger)
	f.backend = NewBackendHandler(f.adapters, f.audit, logger)
	f.servers = NewServerHandler(db, ring, f.adapters, f.audit, logger)
	return f
}

// call runs a handler as an authenticated admin, with the path values the
// router would have extracted.
func (f *backendFixture) call(t *testing.T, h http.HandlerFunc, method, target, body string, path map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("serverId", f.id)
	req.SetPathValue("id", f.id)
	for k, v := range path {
		req.SetPathValue(k, v)
	}
	claims := &auth.Claims{UserID: f.userID, Username: "tester", Role: string(models.RoleAdmin)}
	req = req.WithContext(middleware.WithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()
	middleware.LocaleMiddleware(h).ServeHTTP(rec, req)
	return rec
}

// Every adapter failure kind maps to one status and one translated message,
// whichever route raised it; upstream credential problems never become 401.
func TestBackendErrorContract(t *testing.T) {
	f := newBackendFixture(t)
	cases := []struct {
		name    string
		fail    int
		handler http.HandlerFunc
		path    map[string]string
		status  int
		en, zh  string
	}{
		{"upstream 401", 401, f.backend.ListAccounts, nil, 502, "The server rejected the stored credentials", "服务器拒绝了保存的凭据"},
		{"upstream 403", 403, f.backend.ListAccounts, nil, 502, "The server rejected the stored credentials", "服务器拒绝了保存的凭据"},
		{"upstream 500", 500, f.backend.ListAccounts, nil, 502, "The server failed to complete the operation", "服务器未能完成该操作"},
		{"upstream 429", 429, f.backend.ListAccounts, nil, 503, "The server is rate limiting requests", "服务器正在限流"},
		{"upstream 400", 400, f.backend.ListAccounts, nil, 400, "The server rejected the request", "服务器拒绝了该请求"},
		{"missing account", 0, f.backend.GetAccount, map[string]string{"account": "ghost@example.com"}, 404, "Account not found", "账号不存在"},
		{"missing session", 0, f.backend.TerminateSession, map[string]string{"session": "ghost@example.com/x"}, 404, "Session not found", "会话不存在"},
		{"unsupported rooms", 0, f.backend.ListRooms, nil, 501, "This server does not support the operation", "该服务器不支持此操作"},
	}
	for _, tc := range cases {
		for _, lang := range []string{"en", "zh"} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				f.fake.FailWith(0)
				if _, _, err := f.adapters.Get(context.Background(), f.serverID); err != nil {
					t.Fatalf("probe: %v", err)
				}
				f.fake.FailWith(tc.fail)
				req := httptest.NewRequest("GET", "/", nil)
				req.SetPathValue("serverId", f.id)
				for k, v := range tc.path {
					req.SetPathValue(k, v)
				}
				req.Header.Set("Accept-Language", lang)
				rec := httptest.NewRecorder()
				middleware.LocaleMiddleware(tc.handler).ServeHTTP(rec, req)
				want := tc.en
				if lang == "zh" {
					want = tc.zh
				}
				if rec.Code != tc.status || rec.Body.String() != `{"error":"`+want+`"}`+"\n" {
					t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, want)
				}
			})
		}
	}
	f.fake.FailWith(0)
}

// The server "test" button reports probe failures as a classified message
// with success:false rather than an HTTP error.
func TestServerTestReportsProbeFailures(t *testing.T) {
	f := newBackendFixture(t)
	f.fake.FailWith(401)
	rec := f.call(t, f.servers.Test, "POST", "/", "", nil)
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}
	if body["success"] != false || body["error"] != "The server rejected the stored credentials" {
		t.Fatalf("body = %v", body)
	}
	f.fake.FailWith(0)
	rec = f.call(t, f.servers.Test, "POST", "/", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["success"] != true {
		t.Fatalf("test after fix = %d %s", rec.Code, rec.Body.String())
	}
	if caps, ok := body["capabilities"].([]interface{}); !ok || len(caps) == 0 {
		t.Fatalf("capabilities missing from a successful test: %v", body)
	}
}

// Every mutating backend route writes exactly one audit row with the server
// id in its details.
func TestBackendMutationsAreAudited(t *testing.T) {
	f := newBackendFixture(t)
	steps := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
		path    map[string]string
		status  int
		action  models.AuditAction
		rtype   models.ResourceType
		rid     string
	}{
		{"create", f.backend.CreateAccount, "POST", `{"localpart":"dave","password":"dave-password"}`, nil, 201, models.AuditActionAccountCreate, models.ResourceTypeAccount, "dave@example.com"},
		{"password", f.backend.SetPassword, "PUT", `{"password":"dave-password-2"}`, map[string]string{"account": "dave@example.com"}, 200, models.AuditActionAccountPassword, models.ResourceTypeAccount, "dave@example.com"},
		{"disable", f.backend.SetEnabled, "PUT", `{"enabled":false}`, map[string]string{"account": "dave@example.com"}, 200, models.AuditActionAccountEnabled, models.ResourceTypeAccount, "dave@example.com"},
		{"terminate one", f.backend.TerminateSession, "DELETE", "", map[string]string{"session": "alice@example.com/phone"}, 200, models.AuditActionSessionTerminate, models.ResourceTypeSession, "alice@example.com/phone"},
		{"terminate all", f.backend.TerminateAccountSessions, "DELETE", "", map[string]string{"account": "bob@example.com"}, 200, models.AuditActionSessionTerminate, models.ResourceTypeSession, "bob@example.com"},
		{"delete", f.backend.DeleteAccount, "DELETE", "", map[string]string{"account": "dave@example.com"}, 200, models.AuditActionAccountDelete, models.ResourceTypeAccount, "dave@example.com"},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			rec := f.call(t, step.handler, step.method, "/", step.body, step.path)
			if rec.Code != step.status {
				t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
			}
			var action, rtype, rid string
			var details []byte
			err := f.audit.db.QueryRow(`SELECT action, resource_type, resource_id, details FROM audit_logs ORDER BY id DESC LIMIT 1`).
				Scan(&action, &rtype, &rid, &details)
			if err != nil {
				t.Fatal(err)
			}
			if action != string(step.action) || rtype != string(step.rtype) || rid != step.rid {
				t.Fatalf("audit row = %s %s %s, want %s %s %s", action, rtype, rid, step.action, step.rtype, step.rid)
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal(details, &parsed); err != nil || parsed["server_id"] != float64(f.serverID) {
				t.Fatalf("details = %s", details)
			}
		})
	}
	if users := f.fake.Users(); users["dave"] || !users["alice"] {
		t.Fatalf("upstream state after the run: %v", users)
	}
}

// A failed mutation writes no audit row.
func TestFailedMutationIsNotAudited(t *testing.T) {
	f := newBackendFixture(t)
	rec := f.call(t, f.backend.CreateAccount, "POST", "/", `{"localpart":"alice","password":"alice-password"}`, nil)
	if rec.Code != 409 {
		t.Fatalf("duplicate create = %d %s", rec.Code, rec.Body.String())
	}
	var rows int
	if err := f.audit.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = $1`, models.AuditActionAccountCreate).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d audit rows for a failed create", rows)
	}
}

// Updating or deleting a server drops its cached adapter, so the next call
// uses the new row.
func TestServerWritesInvalidateTheRegistry(t *testing.T) {
	f := newBackendFixture(t)
	if rec := f.call(t, f.backend.ListAccounts, "GET", "/", "", nil); rec.Code != 200 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	rec := f.call(t, f.servers.Update, "PUT", "/", `{"credentials":{"token":"wrong-token"}}`, nil)
	if rec.Code != 200 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.backend.ListAccounts, "GET", "/", "", nil); rec.Code != 502 {
		t.Fatalf("list with the replaced token = %d %s, want 502", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.servers.Update, "PUT", "/", `{"credentials":{"token":"`+prosodytest.Token+`"}}`, nil)
	if rec.Code != 200 {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.backend.ListAccounts, "GET", "/", "", nil); rec.Code != 200 {
		t.Fatalf("list with the restored token = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.servers.Delete, "DELETE", "/", "", nil); rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.backend.ListAccounts, "GET", "/", "", nil); rec.Code != 404 {
		t.Fatalf("list after delete = %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// Listing honours the clamped limit and the cursor it handed out.
func TestListAccountsPages(t *testing.T) {
	f := newBackendFixture(t)
	rec := f.call(t, f.backend.ListAccounts, "GET", "/?limit=2", "", nil)
	var page adapter.Page[adapter.Account]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 {
		t.Fatalf("page 1 = %d %s", rec.Code, rec.Body.String())
	}
	if len(page.Items) != 2 || page.Next == "" || page.Total == nil || *page.Total != 3 {
		t.Fatalf("page 1 = %+v", page)
	}
	rec = f.call(t, f.backend.ListAccounts, "GET", "/?limit=2&cursor="+page.Next, "", nil)
	var last adapter.Page[adapter.Account]
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil || len(last.Items) != 1 || last.Next != "" {
		t.Fatalf("page 2 = %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.backend.ListAccounts, "GET", "/?cursor=nope", "", nil)
	if rec.Code != 400 {
		t.Fatalf("bad cursor = %d %s", rec.Code, rec.Body.String())
	}
}
