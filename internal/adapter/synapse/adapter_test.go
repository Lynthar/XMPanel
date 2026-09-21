package synapse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/synapse/synapsetest"
)

const (
	fakeDomain = "example.com"
	fakeToken  = "syt_admin_token"
)

func newAdapter(endpoint, domain string) *Adapter {
	return New(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: adapter.ImplSynapse, Endpoint: endpoint, Domain: domain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken},
	})
}

// newMASAdapter carries MAS credentials whose endpoint is the fake itself.
func newMASAdapter(endpoint string, secret string) *Adapter {
	return New(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: adapter.ImplSynapse, Endpoint: endpoint, Domain: fakeDomain,
		Creds: adapter.Credentials{
			Kind: adapter.CredentialsBearerMAS, Token: fakeToken,
			MAS: &adapter.MASCredentials{Endpoint: endpoint, ClientID: synapsetest.MASClientID, ClientSecret: secret},
		},
	})
}

// startMAS returns a probed adapter against a fake that delegates to MAS.
func startMAS(t *testing.T) (*Adapter, *synapsetest.Fake) {
	t.Helper()
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.DelegateAuth(true)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	a := newMASAdapter(srv.URL, synapsetest.MASClientSecret)
	t.Cleanup(func() { _ = a.Close() })
	info, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.AuthMode != AuthModeMAS {
		t.Fatalf("auth mode = %q", info.AuthMode)
	}
	return a, fake
}

// start returns a probed adapter against a fresh fake.
func start(t *testing.T) (*Adapter, *synapsetest.Fake) {
	t.Helper()
	fake := synapsetest.New(fakeDomain, fakeToken)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	a := newAdapter(srv.URL, fakeDomain)
	t.Cleanup(func() { _ = a.Close() })
	if _, err := a.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return a, fake
}

func TestContract(t *testing.T) {
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplSynapse,
		Domain:   fakeDomain,
		Upstream: synapsetest.New(fakeDomain, fakeToken),
		New:      func(endpoint string) adapter.Adapter { return newAdapter(endpoint, fakeDomain) },
		Expected: legacyCapabilities,
	})
}

// With MAS credentials the full capability set applies under delegation.
func TestContractWithMAS(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.DelegateAuth(true)
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplSynapse,
		Domain:   fakeDomain,
		Upstream: fake,
		New:      func(endpoint string) adapter.Adapter { return newMASAdapter(endpoint, synapsetest.MASClientSecret) },
		Expected: legacyCapabilities,
	})
}

func TestMASLifecycleGoesThroughMAS(t *testing.T) {
	a, fake := startMAS(t)
	ctx := context.Background()
	if got := a.Capabilities(); len(got) != len(legacyCapabilities) {
		t.Fatalf("capabilities under MAS with credentials = %v", got.Sorted())
	}
	created, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "new", Password: "pw", DisplayName: "New", Admin: true})
	if err != nil || created.ID != "@new:"+fakeDomain || !created.Admin || !created.Enabled || created.DisplayName != "New" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	for _, want := range []string{"POST /api/admin/v1/users", "/set-password", "/set-admin"} {
		if !requestedContaining(fake, want) {
			t.Errorf("create did not use MAS (%s): %v", want, fake.Requests)
		}
	}
	if requested(fake, "PUT /_synapse/admin/v2/users/") {
		t.Errorf("create wrote to Synapse under MAS: %v", fake.Requests)
	}
	got, err := a.GetAccount(ctx, created.ID)
	if err != nil || !got.Admin || !got.Enabled {
		t.Fatalf("get after create = %+v, %v", got, err)
	}
	if err := a.SetEnabled(ctx, created.ID, false); err != nil || !requestedContaining(fake, "/lock") {
		t.Errorf("lock through MAS: %v %v", err, fake.Requests)
	}
	if got, err := a.GetAccount(ctx, created.ID); err != nil || got.Enabled {
		t.Errorf("locked account reads enabled: %+v, %v", got, err)
	}
	if err := a.SetEnabled(ctx, created.ID, true); err != nil || !requestedContaining(fake, "/unlock") {
		t.Errorf("unlock through MAS: %v", err)
	}
	if err := a.SetPassword(ctx, created.ID, "short"); !isKind(err, adapter.Invalid) {
		t.Errorf("weak password must be Invalid through MAS: %v", err)
	}
	if err := a.SetPassword(ctx, created.ID, "long-enough-1"); err != nil {
		t.Errorf("set password through MAS: %v", err)
	}
	if err := a.SetAdmin(ctx, created.ID, false); err != nil {
		t.Errorf("set admin through MAS: %v", err)
	}
	if got, err := a.GetAccount(ctx, created.ID); err != nil || got.Admin {
		t.Errorf("admin bit not cleared: %+v, %v", got, err)
	}
	if err := a.DeleteAccount(ctx, created.ID); err != nil || !requestedContaining(fake, "/deactivate") {
		t.Fatalf("deactivate through MAS: %v", err)
	}
	if requested(fake, "POST /_synapse/admin/v1/deactivate/") {
		t.Errorf("deactivation went to Synapse under MAS: %v", fake.Requests)
	}
	if _, err := a.GetAccount(ctx, created.ID); !isKind(err, adapter.NotFound) {
		t.Errorf("get after deactivate: %v", err)
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "new", Password: "pw"}); !isKind(err, adapter.Conflict) {
		t.Errorf("recreating a taken username: %v", err)
	}
	if err := a.SetPassword(ctx, "@ghost:"+fakeDomain, "long-enough-1"); !isKind(err, adapter.NotFound) {
		t.Errorf("set password on a missing MAS user: %v", err)
	}
}

// MAS never writes its admin bit into Synapse's column, so both the single
// account and the listing must take it from MAS under delegation.
func TestMASAdminBitComesFromMASInListingsToo(t *testing.T) {
	a, fake := startMAS(t)
	ctx := context.Background()
	alice, bob := "@alice:"+fakeDomain, "@bob:"+fakeDomain
	if err := a.SetAdmin(ctx, alice, true); err != nil {
		t.Fatal(err)
	}
	fake.SetHomeserverAdmin(bob, true) // a Synapse-only admin bit, stale under MAS
	page, err := a.ListAccounts(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, acc := range page.Items {
		seen[acc.ID] = acc.Admin
	}
	if !seen[alice] || seen[bob] || !seen["@admin:"+fakeDomain] {
		t.Errorf("listing admin bits = %v, want alice and admin only", seen)
	}
	if got, err := a.GetAccount(ctx, bob); err != nil || got.Admin {
		t.Errorf("single account shows Synapse's stale admin bit: %+v, %v", got, err)
	}
}

// A MAS without password login (an upstream identity provider) can neither
// create an account with a password nor set one.
func TestMASWithoutPasswordLoginNarrowsCapabilities(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.DelegateAuth(true)
	fake.PasswordLogin(false)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newMASAdapter(srv.URL, synapsetest.MASClientSecret)
	defer func() { _ = a.Close() }()
	ctx := context.Background()
	if _, err := a.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	caps := a.Capabilities()
	if caps.Has(adapter.CapAccountsCreate) || caps.Has(adapter.CapAccountsSetPassword) || !caps.Has(adapter.CapAccountsSetEnabled) {
		t.Fatalf("capabilities without password login = %v", caps.Sorted())
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "new", Password: "pw"}); !isKind(err, adapter.NotSupported) {
		t.Errorf("create without password login: %v", err)
	}
	adaptertest.CheckConsistency(t, ctx, a, fake.Reset())
}

// A failure after MAS created the account must not leave it live without
// the password that was asked for.
func TestMASCreateDeactivatesAHalfCreatedAccount(t *testing.T) {
	a, fake := startMAS(t)
	fake.FailSetPassword(true)
	_, err := a.CreateAccount(context.Background(), adapter.CreateAccount{Localpart: "half", Password: "pw"})
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Upstream {
		t.Fatalf("create with a failing set-password: %v", err)
	}
	if !fake.HasUser("@half:"+fakeDomain) || !fake.Deactivated("@half:"+fakeDomain) {
		t.Errorf("half-created account was not deactivated")
	}
}

func TestMASPasswordResetRevokesDevices(t *testing.T) {
	a, fake := startMAS(t)
	ctx := context.Background()
	bob := "@bob:" + fakeDomain
	if err := a.SetPassword(ctx, bob, "long-enough-1"); err != nil {
		t.Fatal(err)
	}
	if devices, err := a.ListAccountSessions(ctx, bob); err != nil || len(devices) != 0 {
		t.Errorf("devices survived a MAS password reset: %+v, %v", devices, err)
	}
	if !requestedContaining(fake, "/delete_devices") {
		t.Errorf("no device revocation was sent: %v", fake.Requests)
	}
}

// One token fetch serves every concurrent caller, and a caller whose own
// context ends does not wait for the fetch.
func TestMASTokenFetchIsSharedAndCancellable(t *testing.T) {
	a, fake := startMAS(t)
	ctx := context.Background()
	fake.SlowToken(300 * time.Millisecond)
	a.mas.mu.Lock()
	a.mas.expiresAt = time.Now()
	a.mas.mu.Unlock()
	before := countRequests(fake, "POST /oauth2/token")
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.GetAccount(ctx, "@alice:"+fakeDomain)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
	if n := countRequests(fake, "POST /oauth2/token") - before; n != 1 {
		t.Errorf("token fetches for five concurrent callers = %d, want 1", n)
	}

	a.mas.mu.Lock()
	a.mas.expiresAt = time.Now()
	a.mas.mu.Unlock()
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = a.GetAccount(ctx, "@alice:"+fakeDomain)
	}()
	time.Sleep(50 * time.Millisecond) // the leader is now inside the slow grant
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := a.GetAccount(short, "@alice:"+fakeDomain)
	if err == nil || time.Since(started) > 200*time.Millisecond {
		t.Errorf("waiter with a dead context: err=%v after %v", err, time.Since(started))
	}
	<-leaderDone
}

func TestMASTokenIsCachedUntilExpiry(t *testing.T) {
	a, fake := startMAS(t)
	ctx := context.Background()
	if _, err := a.GetAccount(ctx, "@alice:"+fakeDomain); err != nil {
		t.Fatal(err)
	}
	if err := a.SetAdmin(ctx, "@alice:"+fakeDomain, true); err != nil {
		t.Fatal(err)
	}
	if n := countRequests(fake, "POST /oauth2/token"); n != 1 {
		t.Fatalf("token requests = %d, want 1 (probe only)", n)
	}
	a.mas.mu.Lock()
	a.mas.expiresAt = time.Now()
	a.mas.mu.Unlock()
	if err := a.SetAdmin(ctx, "@alice:"+fakeDomain, false); err != nil {
		t.Fatal(err)
	}
	if n := countRequests(fake, "POST /oauth2/token"); n != 2 {
		t.Errorf("token requests after expiry = %d, want 2", n)
	}
}

func TestMASRejectsBadClientCredentials(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.DelegateAuth(true)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newMASAdapter(srv.URL, "wrong-secret")
	defer func() { _ = a.Close() }()
	_, err := a.Probe(context.Background())
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Unauthorized || !strings.Contains(err.Error(), "MAS") {
		t.Fatalf("probe with a bad MAS client: %v", err)
	}
}

func TestMASCredentialsAgainstLegacyHomeserverFailProbe(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newMASAdapter(srv.URL, synapsetest.MASClientSecret)
	defer func() { _ = a.Close() }()
	_, err := a.Probe(context.Background())
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Invalid || !strings.Contains(err.Error(), "delegate") {
		t.Fatalf("MAS credentials on a legacy homeserver: %v", err)
	}
}

func TestProbeReportsLegacyMode(t *testing.T) {
	a, fake := start(t)
	info, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.AuthMode != AuthModeLegacy || info.Version != synapsetest.ServerVersion || info.Domains[0] != fakeDomain {
		t.Fatalf("info = %+v", info)
	}
	for _, want := range []string{"GET /_matrix/client/versions", "GET /_matrix/client/v3/account/whoami", "GET /_matrix/client/v1/auth_metadata", "GET /_synapse/admin/v1/server_version"} {
		if !requested(fake, want) {
			t.Errorf("probe did not request %s: %v", want, fake.Requests)
		}
	}
}

// A MAS deployment owns the account lifecycle; until the panel talks to MAS
// those operations are neither declared nor performed against Synapse.
func TestDelegatedAuthNarrowsCapabilities(t *testing.T) {
	a, fake := start(t)
	fake.DelegateAuth(true)
	ctx := context.Background()
	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.AuthMode != AuthModeMAS {
		t.Fatalf("auth mode = %q", info.AuthMode)
	}
	for _, c := range lifecycle {
		if a.Capabilities().Has(c) {
			t.Errorf("%s still declared under MAS", c)
		}
	}
	if !a.Capabilities().Has(adapter.CapAccountsList) || !a.Capabilities().Has(adapter.CapRoomsDelete) {
		t.Errorf("read-side capabilities lost under MAS: %v", a.Capabilities().Sorted())
	}
	pop := fake.Reset()
	adaptertest.CheckConsistency(t, ctx, a, pop)
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "new", Password: "x"}); !isKind(err, adapter.NotSupported) {
		t.Errorf("create under MAS: %v", err)
	}
	if requested(fake, "PUT /_synapse/admin/v2/users/") {
		t.Errorf("a lifecycle write reached Synapse under MAS: %v", fake.Requests)
	}
	fake.DelegateAuth(false)
	if _, err := a.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.Capabilities().Has(adapter.CapAccountsCreate) {
		t.Error("capabilities did not widen again after MAS was turned off")
	}
}

func TestProbeRejectsForeignServerName(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newAdapter(srv.URL, "other.example")
	defer func() { _ = a.Close() }()
	_, err := a.Probe(context.Background())
	failure, ok := adapter.AsError(err)
	if !ok || failure.Kind != adapter.Invalid || !strings.Contains(err.Error(), "other.example") {
		t.Fatalf("probe against the wrong server_name: %v", err)
	}
}

// The create-or-modify PUT must never run for an account that does not
// exist, or a typo in an id would create an account.
func TestModifyingAMissingAccountNeverCreatesIt(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	ghost := "@ghost:" + fakeDomain
	if err := a.SetPassword(ctx, ghost, "new-password-1"); !isKind(err, adapter.NotFound) {
		t.Errorf("set password: %v", err)
	}
	if err := a.SetEnabled(ctx, ghost, false); !isKind(err, adapter.NotFound) {
		t.Errorf("disable: %v", err)
	}
	if err := a.SetAdmin(ctx, ghost, true); !isKind(err, adapter.NotFound) {
		t.Errorf("set admin: %v", err)
	}
	if err := a.DeleteAccount(ctx, ghost); !isKind(err, adapter.NotFound) {
		t.Errorf("delete: %v", err)
	}
	if fake.HasUser(ghost) || requested(fake, "PUT ") {
		t.Errorf("a write reached Synapse for a missing account: %v", fake.Requests)
	}
}

func TestDeactivatedAccountIsGoneButKeepsItsID(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	alice := "@alice:" + fakeDomain
	if err := a.DeleteAccount(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if !fake.HasUser(alice) {
		t.Fatal("deactivation removed the account instead of keeping it")
	}
	if _, err := a.GetAccount(ctx, alice); !isKind(err, adapter.NotFound) {
		t.Errorf("get deactivated: %v", err)
	}
	page, err := a.ListAccounts(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, acc := range page.Items {
		if acc.ID == alice {
			t.Error("deactivated account still listed")
		}
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "alice", Password: "again-1"}); !isKind(err, adapter.Conflict) {
		t.Errorf("recreating a deactivated id: %v", err)
	}
}

func TestListAccountsQueryAndFacts(t *testing.T) {
	a, fake := start(t)
	page, err := a.ListAccounts(context.Background(), adapter.ListQuery{Search: "ali", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "@alice:"+fakeDomain || page.Items[0].Localpart != "alice" || page.Items[0].Domain != fakeDomain {
		t.Fatalf("search result = %+v", page.Items)
	}
	acc := page.Items[0]
	if acc.DisplayName != "Alice" || !acc.Enabled || acc.Admin || acc.Matrix == nil || acc.Matrix.Locked || acc.CreatedAt == nil {
		t.Errorf("account = %+v facts = %+v", acc, acc.Matrix)
	}
	if acc.Matrix.Suspended != nil {
		t.Errorf("listing reported suspended=%v although the listing does not carry it", *acc.Matrix.Suspended)
	}
	last := fake.Requests[len(fake.Requests)-1]
	for _, want := range []string{"guests=false", "deactivated=false", "locked=true", "name=ali", "limit=10", "from=0"} {
		if !strings.Contains(last, want) {
			t.Errorf("listing request %q lacks %s", last, want)
		}
	}
}

func TestLockedAccountReadsDisabled(t *testing.T) {
	a, _ := start(t)
	ctx := context.Background()
	bob := "@bob:" + fakeDomain
	if err := a.SetEnabled(ctx, bob, false); err != nil {
		t.Fatal(err)
	}
	acc, err := a.GetAccount(ctx, bob)
	if err != nil || acc.Enabled || !acc.Matrix.Locked || acc.Matrix.Deactivated {
		t.Fatalf("locked account = %+v %+v, %v", acc, acc.Matrix, err)
	}
	if acc.Matrix.Suspended == nil || *acc.Matrix.Suspended {
		t.Errorf("single-account query must report suspended: %+v", acc.Matrix)
	}
	if acc.CreatedAt == nil || acc.CreatedAt.Year() != 2019 {
		t.Errorf("creation_ts in seconds was not read as such: %v", acc.CreatedAt)
	}
	// Synapse drops locked accounts from the listing unless asked for them;
	// a disabled account must stay visible or it could never be re-enabled.
	page, err := a.ListAccounts(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, item := range page.Items {
		if item.ID == bob {
			listed = true
			if item.Enabled || !item.Matrix.Locked {
				t.Errorf("listed locked account reads enabled: %+v", item)
			}
		}
	}
	if !listed {
		t.Errorf("locked account missing from the listing: %+v", page.Items)
	}
}

func TestDevicesAreSessionsThatAreNeverLive(t *testing.T) {
	a, _ := start(t)
	ctx := context.Background()
	bob := "@bob:" + fakeDomain
	sessions, err := a.ListAccountSessions(ctx, bob)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("devices = %+v, %v", sessions, err)
	}
	for _, s := range sessions {
		if s.AccountID != bob || s.Live {
			t.Errorf("device %+v", s)
		}
	}
	if sessions[0].ID != "DEV2" || sessions[0].Name != "phone" || sessions[0].IP != "10.0.0.2" || sessions[0].LastSeen == nil {
		t.Errorf("device facts = %+v", sessions[0])
	}
	if sessions[1].LastSeen != nil || sessions[1].IP != "" {
		t.Errorf("device without activity = %+v", sessions[1])
	}
	if err := a.TerminateSession(ctx, "", "DEV2"); !isKind(err, adapter.Invalid) {
		t.Errorf("terminate without account: %v", err)
	}
	if _, err := a.ListSessions(ctx, adapter.ListQuery{}); !isKind(err, adapter.NotSupported) {
		t.Errorf("global listing: %v", err)
	}
}

func TestRoomsCarryMatrixFacts(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	page, err := a.ListRooms(ctx, adapter.ListQuery{Limit: 10})
	if err != nil || len(page.Items) != 2 || page.Total == nil || *page.Total != 2 {
		t.Fatalf("rooms = %+v, %v", page, err)
	}
	room := page.Items[1]
	if room.ID != "!room1:"+fakeDomain || room.Name != "Room One" || room.Alias != "#one:"+fakeDomain || room.Members != 2 || !room.Public {
		t.Errorf("room = %+v", room)
	}
	if room.Matrix == nil || room.Matrix.Version != "10" || room.Matrix.Creator != "@alice:"+fakeDomain || room.Matrix.JoinedLocalMembers != 2 {
		t.Errorf("room facts = %+v", room.Matrix)
	}
	if strings.Contains(fake.Requests[len(fake.Requests)-1], "search_term") {
		t.Error("an empty search_term was sent")
	}
	found, err := a.ListRooms(ctx, adapter.ListQuery{Search: "one"})
	if err != nil || len(found.Items) != 1 {
		t.Errorf("search = %+v, %v", found, err)
	}
	if _, err := a.GetRoom(ctx, "room1@conference.example.com"); !isKind(err, adapter.Invalid) {
		t.Errorf("XMPP-shaped room id: %v", err)
	}
}

// The v2 delete would schedule a purge for any well-formed id, so a missing
// room must be caught before it is sent.
func TestDeleteRoomChecksExistenceFirst(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	if err := a.DeleteRoom(ctx, "!ghost:"+fakeDomain); !isKind(err, adapter.NotFound) {
		t.Errorf("delete missing room: %v", err)
	}
	if requested(fake, "DELETE ") {
		t.Errorf("a delete was sent for a missing room: %v", fake.Requests)
	}
	if err := a.DeleteRoom(ctx, "!room2:"+fakeDomain); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetRoom(ctx, "!room2:"+fakeDomain); !isKind(err, adapter.NotFound) {
		t.Errorf("room still present after delete: %v", err)
	}
}

func TestStats(t *testing.T) {
	a, _ := start(t)
	stats, err := a.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Version != synapsetest.ServerVersion || stats.RegisteredUsers == nil || *stats.RegisteredUsers != 3 || stats.Rooms == nil || *stats.Rooms != 2 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.OnlineUsers != nil || stats.ActiveSessions != nil || stats.UptimeSeconds != nil {
		t.Errorf("figures Synapse cannot report were not left nil: %+v", stats)
	}
}

func TestUnknownEndpointIsNotSupportedAndProxy404IsUpstream(t *testing.T) {
	a, fake := start(t)
	ctx := context.Background()
	_, err := a.call(ctx, request{op: "probe", method: http.MethodGet, path: "/_synapse/admin/v1/registration_tokens"}, nil)
	if !isKind(err, adapter.NotSupported) {
		t.Errorf("M_UNRECOGNIZED: %v", err)
	}
	fake.FailWith(http.StatusNotFound)
	_, err = a.Probe(ctx)
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Upstream || failure.Status != http.StatusNotFound {
		t.Errorf("404 without a Matrix body: %v", err)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		status  int
		errcode string
		want    adapter.Kind
	}{
		{401, "M_UNKNOWN_TOKEN", adapter.Unauthorized},
		{401, "", adapter.Unauthorized},
		{403, "M_FORBIDDEN", adapter.Forbidden},
		{404, "M_NOT_FOUND", adapter.NotFound},
		{404, "M_UNRECOGNIZED", adapter.NotSupported},
		{404, "", adapter.Upstream},
		{400, "M_USER_IN_USE", adapter.Conflict},
		{400, "M_INVALID_USERNAME", adapter.Invalid},
		{400, "M_INVALID_PARAM", adapter.Invalid},
		{409, "", adapter.Conflict},
		{429, "M_LIMIT_EXCEEDED", adapter.RateLimited},
		{500, "M_UNKNOWN", adapter.Upstream},
		{502, "", adapter.Upstream},
	} {
		if got := classify(tc.status, tc.errcode); got != tc.want {
			t.Errorf("classify(%d, %q) = %d, want %d", tc.status, tc.errcode, got, tc.want)
		}
	}
}

func TestTimestampUnits(t *testing.T) {
	seconds, millis := timestamp(1560432506), timestamp(1560432506704)
	if seconds.Year() != 2019 || millis.Year() != 2019 || millis.Sub(seconds) != 704*time.Millisecond {
		t.Errorf("seconds → %v, millis → %v", seconds, millis)
	}
}

func TestPageTokenAndFlagShapes(t *testing.T) {
	var page userPage
	if err := jsonUnmarshal(`{"users":[{"name":"@a:x","admin":1,"deactivated":0}],"next_token":"100","total":1}`, &page); err != nil {
		t.Fatal(err)
	}
	if page.NextToken != "100" || !page.Users[0].Admin || page.Users[0].Deactivated {
		t.Errorf("page = %+v", page)
	}
	var rooms roomPage
	if err := jsonUnmarshal(`{"rooms":[],"next_batch":100,"total_rooms":0}`, &rooms); err != nil {
		t.Fatal(err)
	}
	if rooms.NextBatch != "100" {
		t.Errorf("next_batch = %q", rooms.NextBatch)
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func requestedContaining(f *synapsetest.Fake, part string) bool {
	for _, r := range f.Requests {
		if strings.Contains(r, part) {
			return true
		}
	}
	return false
}

func countRequests(f *synapsetest.Fake, prefix string) int {
	n := 0
	for _, r := range f.Requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func requested(f *synapsetest.Fake, prefix string) bool {
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
