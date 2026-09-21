package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/adapter/synapse/synapsetest"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/store/models"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

const (
	matrixDomain = "example.com"
	matrixToken  = "syt_handler_token"
)

type matrixFixture struct {
	fake     *synapsetest.Fake
	matrix   *MatrixHandler
	audit    *AuditService
	id       string
	serverID int64
	userID   int64
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	db := newTestDB(t)
	ring := storetest.NewKeyRing(t)
	f := &matrixFixture{fake: synapsetest.New(matrixDomain, matrixToken)}
	upstream := httptest.NewServer(f.fake)
	t.Cleanup(upstream.Close)
	f.serverID = storetest.InsertServer(t, db, ring, "synapse", upstream.URL, matrixDomain, matrixToken)
	f.id = strconv.FormatInt(f.serverID, 10)
	err := db.QueryRow(`INSERT INTO users (username, email, password_hash, role) VALUES ('matrix-tester', 'matrix-tester@localhost', 'x', 'admin')
		ON CONFLICT (username) DO UPDATE SET role = 'admin' RETURNING id`).Scan(&f.userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, f.userID) })
	logger := zap.NewNop()
	adapters := registry.New(db, ring, logger)
	t.Cleanup(adapters.Close)
	f.audit = NewAuditService(db, logger)
	f.matrix = NewMatrixHandler(adapters, f.audit, logger)
	return f
}

func (f *matrixFixture) call(t *testing.T, h http.HandlerFunc, method, target, body string, path map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("serverId", f.id)
	for k, v := range path {
		req.SetPathValue(k, v)
	}
	claims := &auth.Claims{UserID: f.userID, Username: "tester", Role: string(models.RoleAdmin)}
	req = req.WithContext(middleware.WithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()
	middleware.LocaleMiddleware(h).ServeHTTP(rec, req)
	return rec
}

// An XMPP server has no MatrixAdmin, so every matrix route answers 501
// with the same translated message.
func TestMatrixRoutesNeedAMatrixAdmin(t *testing.T) {
	f := newBackendFixture(t)
	matrix := NewMatrixHandler(f.adapters, f.audit, zap.NewNop())
	for _, lang := range []string{"en", "zh"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("serverId", f.id)
		req.Header.Set("Accept-Language", lang)
		rec := httptest.NewRecorder()
		middleware.LocaleMiddleware(http.HandlerFunc(matrix.ListRegistrationTokens)).ServeHTTP(rec, req)
		want := `{"error":"This server has no Matrix moderation tools"}`
		if lang == "zh" {
			want = `{"error":"该服务器没有 Matrix 管理工具"}`
		}
		if rec.Code != http.StatusNotImplemented || rec.Body.String() != want+"\n" {
			t.Errorf("%s: %d %s", lang, rec.Code, rec.Body.String())
		}
	}
}

// Every mutating matrix route writes exactly one audit row with the server
// id and protocol in its details; a registration token never lands in one.
func TestMatrixMutationsAreAudited(t *testing.T) {
	f := newMatrixFixture(t)
	alice, bob := "@alice:"+matrixDomain, "@bob:"+matrixDomain
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
		key     string
		value   interface{}
	}{
		{"suspend", f.matrix.SetSuspended, "PUT", `{"suspended":true}`, map[string]string{"account": bob}, 200, models.AuditActionMatrixSuspend, models.ResourceTypeAccount, bob, "suspended", true},
		{"shadow ban", f.matrix.SetShadowBanned, "PUT", `{"banned":true}`, map[string]string{"account": bob}, 200, models.AuditActionMatrixShadowBan, models.ResourceTypeAccount, bob, "banned", true},
		{"create token", f.matrix.CreateRegistrationToken, "POST", `{"token":"handler-token","uses_allowed":5}`, nil, 201, models.AuditActionMatrixRegTokenCreate, models.ResourceTypeToken, "hand*********", "uses_allowed", float64(5)},
		{"delete token", f.matrix.DeleteRegistrationToken, "DELETE", "", map[string]string{"token": "handler-token"}, 200, models.AuditActionMatrixRegTokenDelete, models.ResourceTypeToken, "hand*********", "server_id", float64(0)},
		{"quarantine", f.matrix.QuarantineAccountMedia, "POST", "", map[string]string{"account": "@admin:" + matrixDomain}, 200, models.AuditActionMatrixMediaQuarantine, models.ResourceTypeAccount, "@admin:" + matrixDomain, "quarantined", float64(2)},
		{"delete media", f.matrix.DeleteMedia, "DELETE", "", map[string]string{"mediaId": "MEDIA1"}, 200, models.AuditActionMatrixMediaDelete, models.ResourceTypeMedia, "MEDIA1", "server_id", float64(0)},
		{"block", f.matrix.BlockRoom, "POST", `{"block":true}`, map[string]string{"room": "!room1:" + matrixDomain}, 200, models.AuditActionMatrixRoomBlock, models.ResourceTypeRoom, "!room1:" + matrixDomain, "block", true},
		{"purge", f.matrix.PurgeRoom, "POST", `{"purge":true,"block":true}`, map[string]string{"room": "!room2:" + matrixDomain}, 202, models.AuditActionMatrixRoomPurge, models.ResourceTypeRoom, "!room2:" + matrixDomain, "delete_id", "delete-1"},
		{"notice", f.matrix.SendNotice, "POST", `{"account":"` + bob + `","body":"maintenance tonight"}`, nil, 200, models.AuditActionMatrixNotice, models.ResourceTypeAccount, bob, "length", float64(19)},
		{"deactivate", f.matrix.Deactivate, "POST", `{"erase":true}`, map[string]string{"account": alice}, 200, models.AuditActionMatrixDeactivate, models.ResourceTypeAccount, alice, "erase", true},
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
			if err := json.Unmarshal(details, &parsed); err != nil || parsed["server_id"] != float64(f.serverID) || parsed["protocol"] != "matrix" {
				t.Fatalf("details = %s", details)
			}
			want := step.value
			if step.key == "server_id" {
				want = float64(f.serverID)
			}
			if parsed[step.key] != want || strings.Contains(string(details), "handler-token") {
				t.Fatalf("details = %s, want %s=%v and no token", details, step.key, want)
			}
		})
	}
	if !f.fake.Erased(alice) || len(f.fake.Notices(bob)) != 1 || !f.fake.Blocked("!room1:"+matrixDomain) {
		t.Errorf("upstream state after the run: erased=%v notices=%v blocked=%v", f.fake.Erased(alice), f.fake.Notices(bob), f.fake.Blocked("!room1:"+matrixDomain))
	}
}

func TestMatrixFailuresAreNotAuditedAndValidated(t *testing.T) {
	f := newMatrixFixture(t)
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
		path    map[string]string
		status  int
	}{
		{"deactivate missing", f.matrix.Deactivate, "POST", `{"erase":false}`, map[string]string{"account": "@ghost:" + matrixDomain}, 404},
		{"suspend without flag", f.matrix.SetSuspended, "PUT", `{}`, map[string]string{"account": "@bob:" + matrixDomain}, 400},
		{"negative uses", f.matrix.CreateRegistrationToken, "POST", `{"uses_allowed":-1}`, nil, 400},
		{"zero uses", f.matrix.CreateRegistrationToken, "POST", `{"uses_allowed":0}`, nil, 400},
		{"duplicate token", f.matrix.CreateRegistrationToken, "POST", `{"token":"seed-token"}`, nil, 409},
		{"delete missing token", f.matrix.DeleteRegistrationToken, "DELETE", "", map[string]string{"token": "nope"}, 404},
		{"delete missing media", f.matrix.DeleteMedia, "DELETE", "", map[string]string{"mediaId": "nope"}, 404},
		{"notice without body", f.matrix.SendNotice, "POST", `{"account":"@bob:` + matrixDomain + `"}`, nil, 400},
		{"purge missing room", f.matrix.PurgeRoom, "POST", `{"purge":true}`, map[string]string{"room": "!ghost:" + matrixDomain}, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.call(t, tc.handler, tc.method, "/", tc.body, tc.path)
			if rec.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}
	var rows int
	if err := f.audit.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'matrix.%'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d audit rows for failed matrix mutations", rows)
	}
}

func TestMatrixListingsPage(t *testing.T) {
	f := newMatrixFixture(t)
	rec := f.call(t, f.matrix.ListAccountMedia, "GET", "/?limit=1", "", map[string]string{"account": "@admin:" + matrixDomain})
	var page struct {
		Items []map[string]interface{} `json:"items"`
		Next  string                   `json:"next"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || len(page.Items) != 1 || page.Next != "1" || page.Total != 2 {
		t.Fatalf("media page = %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.matrix.ListReports, "GET", "/", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"reporter":"@bob:example.com"`) {
		t.Fatalf("reports = %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.matrix.ListFederation, "GET", "/?cursor=x", "", nil)
	if rec.Code != 400 {
		t.Fatalf("bad cursor = %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.matrix.ListRegistrationTokens, "GET", "/", "", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "[") {
		t.Fatalf("tokens = %d %s", rec.Code, rec.Body.String())
	}
}
