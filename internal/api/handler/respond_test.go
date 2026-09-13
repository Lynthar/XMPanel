package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/api/middleware"

	"go.uber.org/zap"
)

// Only respond.go writes a status line or encodes a body straight to w. A
// handler that does so itself is a second response path, and the next locale
// or shape change misses it.
func TestOnlyRespondWritesStatusLines(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file == "respond.go" || strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, probe := range []string{"http.Error(", ".WriteHeader(", "json.NewEncoder(w)"} {
				if strings.Contains(line, probe) {
					t.Errorf("%s:%d writes a response outside respond.go: %s", file, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}

// Every error leaves as {"error": text} plus a newline, application/json, in
// the request's locale. Each case stops before the first database call, so
// the handlers run on nil stores; database-dependent branches are checked by hand.
func TestErrorResponseShape(t *testing.T) {
	logger := zap.NewNop()
	xmppH := NewXMPPHandler(nil, nil, nil, logger)
	serverH := NewServerHandler(nil, nil, nil, logger)
	userH := NewUserHandler(nil, nil, nil, nil, nil, logger)
	auditH := NewAuditHandler(nil, logger)
	authH := NewAuthHandler(nil, nil, nil, nil, nil, nil, 0, false, logger)

	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		target  string
		path    map[string]string
		body    string
		lang    string
		status  int
		want    string
	}{
		{"xmpp: bad server id", xmppH.ListUsers, "GET", "/", map[string]string{"serverId": "x"}, "", "", 400, "Invalid server ID"},
		{"xmpp: missing domain", xmppH.ListUsers, "GET", "/", map[string]string{"serverId": "1"}, "", "", 400, "Domain parameter is required"},
		{"xmpp: bad body", xmppH.CreateUser, "POST", "/", map[string]string{"serverId": "1"}, "{", "", 400, "Invalid request body"},
		{"xmpp: short password", xmppH.CreateUser, "POST", "/", map[string]string{"serverId": "1"}, `{"username":"a","domain":"d","password":"short"}`, "", 400, "Password must be at least 8 characters"},
		{"xmpp: missing muc domain", xmppH.ListRooms, "GET", "/", map[string]string{"serverId": "1"}, "", "", 400, "MUC domain parameter is required"},
		{"server: bad id", serverH.Get, "GET", "/", map[string]string{"id": "abc"}, "", "", 400, "Invalid server ID"},
		{"server: bad port", serverH.Create, "POST", "/", nil, `{"name":"n","host":"h","port":70000}`, "", 400, "Invalid port"},
		{"server: bad type", serverH.Create, "POST", "/", nil, `{"name":"n","host":"h","port":5280,"type":"nope"}`, "", 400, "Invalid server type"},
		{"user: bad id", userH.Get, "GET", "/", map[string]string{"id": "abc"}, "", "", 400, "Invalid user ID"},
		{"user: create without claims", userH.Create, "POST", "/", nil, "{}", "", 401, "Unauthorized"},
		{"user: update without claims", userH.Update, "PUT", "/", map[string]string{"id": "1"}, "{}", "", 401, "Unauthorized"},
		{"audit: bad details filter", auditH.List, "GET", "/audit?details_contains=notjson", nil, "", "", 400, "details_contains must be valid JSON"},
		{"auth: bad login body, default locale", authH.Login, "POST", "/", nil, "{", "", 400, "Bad request"},
		{"auth: bad login body, zh", authH.Login, "POST", "/", nil, "{", "zh-CN,zh;q=0.9", 400, "请求格式错误"},
		{"auth: logout without claims", authH.Logout, "POST", "/", nil, "", "", 401, "Authentication required"},
		{"auth: me without claims, zh", authH.Me, "GET", "/", nil, "", "zh", 401, "请先登录"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			for k, v := range tc.path {
				req.SetPathValue(k, v)
			}
			if tc.lang != "" {
				req.Header.Set("Accept-Language", tc.lang)
			}
			rec := httptest.NewRecorder()
			middleware.LocaleMiddleware(tc.handler).ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got, want := rec.Body.String(), `{"error":"`+tc.want+`"}`+"\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}
