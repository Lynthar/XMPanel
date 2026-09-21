package matrixgeneric

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/matrixgeneric/generictest"
)

const (
	fakeDomain = "example.com"
	fakeToken  = "syt_admin_token"
)

func newAdapter(endpoint, domain string, creds adapter.Credentials) *Adapter {
	return New(adapter.ServerConfig{Protocol: adapter.ProtocolMatrix, Impl: adapter.ImplMatrixGeneric, Endpoint: endpoint, Domain: domain, Creds: creds})
}

func bearer() adapter.Credentials {
	return adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken}
}

// start returns a probed adapter against a fresh fake.
func start(t *testing.T) (*Adapter, *generictest.Fake) {
	t.Helper()
	fake := generictest.New(fakeDomain, fakeToken)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	a := newAdapter(srv.URL, fakeDomain, bearer())
	t.Cleanup(func() { _ = a.Close() })
	if _, err := a.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return a, fake
}

func TestContract(t *testing.T) {
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplMatrixGeneric,
		Domain:   fakeDomain,
		Upstream: generictest.New(fakeDomain, fakeToken),
		New:      func(endpoint string) adapter.Adapter { return newAdapter(endpoint, fakeDomain, bearer()) },
		Expected: adapter.NewCapabilitySet(adapter.CapAccountsGet, adapter.CapAccountsSetEnabled, adapter.CapMatrixSuspend, adapter.CapSessionsListByAcct),
	})
}

// The probe reads m.account_moderation and whois to decide the capability
// set, takes the version from the vendor route when federation is off, and
// never consults auth_metadata.
func TestProbeDeclaresWhatTheServerAdvertises(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Impl != adapter.ImplMatrixGeneric || info.Version != generictest.Version || info.AuthMode != "" || info.Domains[0] != fakeDomain {
		t.Fatalf("info = %+v", info)
	}
	for _, r := range fake.Requests {
		if strings.Contains(r, "auth_metadata") {
			t.Errorf("auth_metadata was requested")
		}
	}
	if !requested(fake, "GET /_continuwuity/server_version") {
		t.Errorf("vendor version route not tried: %v", fake.Requests)
	}

	fake.Federation(true)
	if info, err := a.Probe(ctx); err != nil || info.Version != generictest.Version {
		t.Errorf("version with federation on: %+v, %v", info, err)
	}
	fake.Whois(false)
	if _, err := a.Probe(ctx); err != nil || a.Capabilities().Has(adapter.CapSessionsListByAcct) {
		t.Errorf("whois unserved but sessions declared: %v %v", err, a.Capabilities().Sorted())
	}
	if _, err := a.ListAccountSessions(ctx, "@alice:"+fakeDomain); !isKind(err, adapter.NotSupported) {
		t.Errorf("sessions without whois: %v", err)
	}
	fake.Moderation(false, true)
	if _, err := a.Probe(ctx); err != nil || a.Capabilities().Has(adapter.CapAccountsSetEnabled) || !a.Capabilities().Has(adapter.CapMatrixSuspend) {
		t.Errorf("lock unadvertised: %v %v", err, a.Capabilities().Sorted())
	}
	if err := a.SetEnabled(ctx, "@alice:"+fakeDomain, false); !isKind(err, adapter.NotSupported) {
		t.Errorf("lock without the capability: %v", err)
	}
	fake.Moderation(false, false)
	if _, err := a.Probe(ctx); err != nil || len(a.Capabilities()) != 1 || !a.Capabilities().Has(adapter.CapAccountsGet) {
		t.Errorf("no moderation: %v %v", err, a.Capabilities().Sorted())
	}
	pop := fake.Reset()
	adaptertest.CheckConsistency(t, ctx, a, pop)
	// Without moderation the account is known from its profile alone.
	if got, err := a.GetAccount(ctx, pop.Accounts[0]); err != nil || !got.Enabled || got.DisplayName != "Alice" || got.Matrix.Suspended != nil {
		t.Errorf("profile-only account: %+v, %v", got, err)
	}

	// A token without admin rights sees no moderation and no whois; the
	// probe passes with the lookup alone rather than failing.
	fake.Moderation(true, true)
	fake.Whois(true)
	fake.SetAdmin(false)
	if _, err := a.Probe(ctx); err != nil || len(a.Capabilities()) != 1 {
		t.Errorf("non-admin token: %v %v", err, a.Capabilities().Sorted())
	}
}

func TestProbeRefusesMASCredentialsAndForeignServerName(t *testing.T) {
	fake := generictest.New(fakeDomain, fakeToken)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	withMAS := newAdapter(srv.URL, fakeDomain, adapter.Credentials{Kind: adapter.CredentialsBearerMAS, Token: fakeToken, MAS: &adapter.MASCredentials{Endpoint: srv.URL, ClientID: "x", ClientSecret: "y"}})
	defer func() { _ = withMAS.Close() }()
	if _, err := withMAS.Probe(context.Background()); !isKind(err, adapter.Invalid) {
		t.Errorf("MAS credentials: %v", err)
	}
	foreign := newAdapter(srv.URL, "other.example", bearer())
	defer func() { _ = foreign.Close() }()
	if _, err := foreign.Probe(context.Background()); !isKind(err, adapter.Invalid) || !strings.Contains(err.Error(), "other.example") {
		t.Errorf("wrong server_name: %v", err)
	}
}

// Lock and suspend go through the spec routes and read back; a missing
// account is NotFound from the moderation route, and the spec refuses to
// moderate an admin.
func TestLookupLockAndSuspend(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	alice := "@alice:" + fakeDomain
	got, err := a.GetAccount(ctx, "alice")
	if err != nil || got.ID != alice || !got.Enabled || got.DisplayName != "Alice" || got.Matrix == nil || got.Matrix.Locked || got.Matrix.Suspended == nil || *got.Matrix.Suspended {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if err := a.SetEnabled(ctx, alice, false); err != nil || !fake.Locked(alice) {
		t.Fatalf("lock: %v", err)
	}
	if got, err := a.GetAccount(ctx, alice); err != nil || got.Enabled || !got.Matrix.Locked {
		t.Errorf("locked account reads %+v, %v", got, err)
	}
	if err := a.SetEnabled(ctx, alice, true); err != nil || fake.Locked(alice) {
		t.Errorf("unlock: %v", err)
	}
	if err := a.SetSuspended(ctx, alice, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if got, err := a.GetAccount(ctx, alice); err != nil || got.Matrix.Suspended == nil || !*got.Matrix.Suspended || !got.Enabled {
		t.Errorf("suspended account reads %+v, %v", got, err)
	}
	if _, err := a.GetAccount(ctx, "@ghost:"+fakeDomain); !isKind(err, adapter.NotFound) {
		t.Errorf("missing account: %v", err)
	}
	if err := a.SetEnabled(ctx, "@ghost:"+fakeDomain, false); !isKind(err, adapter.NotFound) {
		t.Errorf("lock missing account: %v", err)
	}
	if err := a.SetEnabled(ctx, "@"+generictest.AdminLocalpart+":"+fakeDomain, false); !isKind(err, adapter.Forbidden) {
		t.Errorf("locking an admin: %v", err)
	}
	if _, err := a.GetAccount(ctx, "@bad"); !isKind(err, adapter.Invalid) {
		t.Errorf("malformed id: %v", err)
	}
}

// whois connections become sessions: one per connection, numbered, keyed
// by device id when the server attributes them and never live.
func TestSessionsComeFromWhois(t *testing.T) {
	a, _ := start(t)
	ctx := context.Background()
	alice := "@alice:" + fakeDomain
	sessions, err := a.ListAccountSessions(ctx, alice)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	first := sessions[0]
	if first.ID != "connection-1" || first.AccountID != alice || first.IP != "10.0.0.1" || first.UserAgent != "Element/1.11" || first.LastSeen == nil || first.Live || first.Name != "" {
		t.Errorf("first session = %+v", first)
	}
	bobs, err := a.ListAccountSessions(ctx, "bob")
	if err != nil || len(bobs) != 1 || bobs[0].ID != "PHONE/connection-1" || bobs[0].Name != "PHONE" || bobs[0].LastSeen != nil {
		t.Errorf("bob sessions = %+v, %v", bobs, err)
	}
	if _, err := a.ListAccountSessions(ctx, "@ghost:"+fakeDomain); !isKind(err, adapter.NotFound) {
		t.Errorf("whois on a missing account: %v", err)
	}
}

// Stats carries the version always and the vendor user count only when
// federation exposes it.
func TestStats(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	stats, err := a.Stats(ctx)
	if err != nil || stats.Version != generictest.Version || stats.RegisteredUsers != nil || stats.Rooms != nil {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	fake.Federation(true)
	if stats, err := a.Stats(ctx); err != nil || stats.RegisteredUsers == nil || *stats.RegisteredUsers != 3 {
		t.Errorf("stats with federation = %+v, %v", stats, err)
	}
}

func requested(f *generictest.Fake, prefix string) bool {
	for _, r := range f.Requests {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func isKind(err error, kind adapter.Kind) bool {
	failure, ok := adapter.AsError(err)
	return ok && failure.Kind == kind
}
