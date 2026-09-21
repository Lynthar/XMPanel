package prosody

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/prosody/prosodytest"
)

func newAdapter(endpoint string) adapter.Adapter {
	return New(adapter.ServerConfig{
		Protocol: adapter.ProtocolXMPP, Impl: adapter.ImplProsody,
		Endpoint: endpoint, Domain: prosodytest.Domain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: prosodytest.Token},
	})
}

func TestContract(t *testing.T) {
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolXMPP,
		Impl:     adapter.ImplProsody,
		Domain:   prosodytest.Domain,
		Upstream: prosodytest.NewFake(),
		New:      newAdapter,
		Expected: baseCapabilities,
	})
}

// The Host header carries the VirtualHost name even when the endpoint is a
// loopback address, because Prosody routes by Host.
func TestHostHeaderIsTheDomain(t *testing.T) {
	fake := prosodytest.NewFake()
	fake.Reset()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newAdapter(srv.URL)
	if _, err := a.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, host := range fake.Hosts() {
		if host != prosodytest.Domain {
			t.Fatalf("Host header = %q, want %q", host, prosodytest.Domain)
		}
	}
}

// Without mod_admin_panel the adapter still probes and reports stats, but
// declares no account or session capability and answers NotSupported-free
// upstream errors for them, so the missing module shows up as an install gap.
func TestMissingAdminPanelNarrowsCapabilities(t *testing.T) {
	fake := prosodytest.NewFake()
	fake.AdminPanel = false
	fake.Reset()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newAdapter(srv.URL)
	ctx := context.Background()
	if _, err := a.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if caps := a.Capabilities(); len(caps) != 0 {
		t.Fatalf("capabilities = %v, want none", caps.Sorted())
	}
	stats, err := a.Stats(ctx)
	if err != nil || stats.RegisteredUsers == nil || *stats.RegisteredUsers != 3 || stats.OnlineUsers != nil {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	_, err = a.ListAccounts(ctx, adapter.ListQuery{})
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Upstream || failure.Status != http.StatusNotFound {
		t.Fatalf("list without module: %v", err)
	}
}

func TestSessionTimestampsAndFacts(t *testing.T) {
	fake := prosodytest.NewFake()
	fake.Reset()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newAdapter(srv.URL)
	ctx := context.Background()
	if _, err := a.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListSessions(ctx, adapter.ListQuery{Search: "desktop"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("search: %v %+v", err, page)
	}
	s := page.Items[0]
	if s.ID != "alice@example.com/desktop/2" || s.AccountID != "alice@example.com" || s.Name != "desktop/2" ||
		s.StartedAt == nil || s.StartedAt.Year() != 2026 || s.XMPP == nil || s.XMPP.Priority != 1 || !s.Live {
		t.Fatalf("session = %+v", s)
	}
	if err := a.TerminateSession(ctx, s.AccountID, s.ID); err != nil {
		t.Fatalf("terminate a resource containing a slash: %v", err)
	}
}
