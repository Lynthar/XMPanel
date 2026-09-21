package handler

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// These responses are the public contract for the existing XMPP routes.
// The fake upstream controls failures after the database lookup; assertions
// cover status, content type, translation and exact JSON bytes.
func TestAdapterHTTPErrorResponses(t *testing.T) {
	db := newTestDB(t)
	var upstreamStatus atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(upstreamStatus.Load()))
		_, _ = w.Write([]byte("upstream failure"))
	}))
	defer upstream.Close()
	host, portString, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	logger := zap.NewNop()
	adapters := registry.New(db, nil, logger)
	defer adapters.Close()
	xmpp := NewXMPPHandler(adapters, nil, logger)
	server := NewServerHandler(db, nil, adapters, nil, logger)
	cases := []struct {
		name    string
		method  string
		handler http.HandlerFunc
		body    string
		message string
	}{
		{"list users", "GET", xmpp.ListUsers, "", "Failed to list users"},
		{"get user", "GET", xmpp.GetUser, "", "Failed to get user"},
		{"create user", "POST", xmpp.CreateUser, `{"username":"alice","domain":"example.com","password":"test-password"}`, "Failed to create user"},
		{"delete user", "DELETE", xmpp.DeleteUser, "", "Failed to delete user"},
		{"kick user", "POST", xmpp.KickUser, "", "Failed to kick user"},
		{"list sessions", "GET", xmpp.ListSessions, "", "Failed to list sessions"},
		{"kick session", "DELETE", xmpp.KickSession, "", "Failed to kick session"},
		{"list rooms", "GET", xmpp.ListRooms, "", "Failed to list rooms"},
		{"get room", "GET", xmpp.GetRoom, "", "Failed to get room"},
		{"create room", "POST", xmpp.CreateRoom, `{"name":"room","domain":"conference.example.com"}`, "Failed to create room"},
		{"delete room", "DELETE", xmpp.DeleteRoom, "", "Failed to delete room"},
		{"stats", "GET", server.Stats, "", "Failed to get server statistics"},
	}
	for _, kind := range []models.ServerType{models.ServerTypeProsody, models.ServerTypeEjabberd} {
		var id int64
		err := db.QueryRow(`INSERT INTO xmpp_servers (name, type, host, port, tls_enabled)
			VALUES ($1, $2, $3, $4, FALSE) RETURNING id`, "response fixture", kind, host, port).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		idString := strconv.FormatInt(id, 10)
		for _, status := range []int{401, 403, 404, 409, 429, 500} {
			upstreamStatus.Store(int64(status))
			for _, tc := range cases {
				for _, language := range []string{"en", "zh"} {
					t.Run(fmt.Sprintf("%s/%d/%s/%s", kind, status, tc.name, language), func(t *testing.T) {
						wantStatus, message := http.StatusBadGateway, tc.message
						if status == 404 && (tc.name == "delete user" || (tc.name == "get user" && kind == models.ServerTypeEjabberd)) {
							wantStatus, message = http.StatusNotFound, "User not found"
							if language == "zh" {
								message = "用户不存在"
							}
						}
						if status == 409 && tc.name == "create user" && kind == models.ServerTypeProsody {
							wantStatus, message = http.StatusConflict, "User already exists"
						}
						want := `{"error":"` + message + `"}` + "\n"
						if tc.name == "stats" && kind == models.ServerTypeEjabberd {
							wantStatus = http.StatusOK
							want = `{"online_users":0,"registered_users":0,"active_sessions":0,"s2s_connections":0,"uptime_seconds":0,"version":""}` + "\n"
						}
						req := httptest.NewRequest(tc.method, "/?domain=example.com&muc_domain=conference.example.com", strings.NewReader(tc.body))
						req.SetPathValue("id", idString)
						req.SetPathValue("serverId", idString)
						req.SetPathValue("username", "alice")
						req.SetPathValue("room", "room")
						req.SetPathValue("jid", "alice@example.com/resource")
						req.Header.Set("Accept-Language", language)
						rec := httptest.NewRecorder()
						middleware.LocaleMiddleware(tc.handler).ServeHTTP(rec, req)
						if rec.Code != wantStatus || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != want {
							t.Fatalf("response = %d %q %q, want %d application/json %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(), wantStatus, want)
						}
					})
				}
			}
		}
		if _, err := db.Exec(`DELETE FROM xmpp_servers WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
}
