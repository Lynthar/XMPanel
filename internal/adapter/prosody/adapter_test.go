package prosody

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/store/models"
)

func fixtureAdapter(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAdapter(&models.XMPPServer{Host: host, Port: port}, "fixture-token")
	t.Cleanup(func() { _ = a.Disconnect() })
	return a
}

func TestUpstreamErrorsRetainClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   adapter.Kind
	}{
		{401, adapter.Unauthorized},
		{403, adapter.Forbidden},
		{404, adapter.Upstream},
		{409, adapter.Conflict},
		{500, adapter.Upstream},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			var a *Adapter
			a = fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				wantHost := net.JoinHostPort(a.server.Host, strconv.Itoa(a.server.Port))
				if r.Host != wantHost || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Errorf("upstream headers do not match the configured host and credentials")
				}
				w.WriteHeader(tc.status)
			})
			err := a.Ping(context.Background())
			var failure *adapter.Error
			if !errors.As(err, &failure) {
				t.Fatalf("unclassified failure: %v", err)
			}
			if failure.Kind != tc.kind || failure.Status != tc.status || failure.Op != "server.ping" ||
				failure.Resource != "/admin_api/server/info" {
				t.Fatalf("failure metadata = %+v", failure)
			}
		})
	}
}

func TestCanceledRequestPreservesItsCause(t *testing.T) {
	a := fixtureAdapter(t, func(http.ResponseWriter, *http.Request) {
		t.Error("canceled request reached the upstream")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.Ping(ctx)
	var failure *adapter.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.Kind != adapter.Unreachable {
		t.Fatalf("cancellation cause was lost: %v", err)
	}
}

func TestSessionsTranslateLuaTimestampsWithoutChangingJSON(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin_panel/sessions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
            {"jid":"alice@example.com/phone","bare_jid":"alice@example.com","resource":"phone","ip_address":"192.0.2.1","secure":true,"priority":1,"status":"away","connected_at":"2026-09-20T12:00:00Z"},
            {"jid":"bob@example.com/desktop","resource":"desktop","ip_address":"192.0.2.2","priority":0,"status":"online"}
        ]`))
	})
	sessions, err := a.GetOnlineSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"jid":"alice@example.com/phone","resource":"phone","ip_address":"192.0.2.1","priority":1,"status":"away","started_at":"2026-09-20T12:00:00Z"},{"jid":"bob@example.com/desktop","resource":"desktop","ip_address":"192.0.2.2","priority":0,"status":"online","started_at":"0001-01-01T00:00:00Z"}]`
	if string(body) != want {
		t.Fatalf("wire response = %s, want %s", body, want)
	}
}
