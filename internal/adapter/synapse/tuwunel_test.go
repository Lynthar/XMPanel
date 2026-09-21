package synapse

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/synapse/synapsetest"
)

func newTuwunelAdapter(endpoint string, creds adapter.Credentials) *Adapter {
	return New(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: adapter.ImplTuwunel, Endpoint: endpoint, Domain: fakeDomain, Creds: creds,
	})
}

// startTuwunel returns a probed adapter against a fake answering as Tuwunel.
func startTuwunel(t *testing.T) (*Adapter, *synapsetest.Fake) {
	t.Helper()
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.Tuwunel(true)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	a := newTuwunelAdapter(srv.URL, adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken})
	t.Cleanup(func() { _ = a.Close() })
	if _, err := a.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return a, fake
}

func TestContractTuwunel(t *testing.T) {
	fake := synapsetest.New(fakeDomain, fakeToken)
	fake.Tuwunel(true)
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplTuwunel,
		Domain:   fakeDomain,
		Upstream: fake,
		New: func(endpoint string) adapter.Adapter {
			return newTuwunelAdapter(endpoint, adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken})
		},
		Expected: legacyCapabilities.Without(tuwunelMask...),
	})
}

// Tuwunel is probed without the delegation step: its own OIDC server would
// answer auth_metadata, and MAS credentials are refused up front.
func TestTuwunelProbeSkipsDelegationAndRefusesMAS(t *testing.T) {
	a, fake := startTuwunel(t)
	info, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Impl != adapter.ImplTuwunel || info.AuthMode != AuthModeLegacy || info.Version != synapsetest.TuwunelVersion {
		t.Fatalf("info = %+v", info)
	}
	if requested(fake, "GET /_matrix/client/v1/auth_metadata") {
		t.Errorf("auth_metadata was requested: %v", fake.Requests)
	}
	for _, c := range tuwunelMask {
		if a.Capabilities().Has(c) {
			t.Errorf("%s declared for Tuwunel", c)
		}
	}
	if !a.Capabilities().Has(adapter.CapAccountsSetAdmin) || !a.Capabilities().Has(adapter.CapMatrixMedia) {
		t.Errorf("capabilities = %v", a.Capabilities().Sorted())
	}

	srv := httptest.NewServer(fake)
	defer srv.Close()
	withMAS := newTuwunelAdapter(srv.URL, adapter.Credentials{
		Kind: adapter.CredentialsBearerMAS, Token: fakeToken,
		MAS: &adapter.MASCredentials{Endpoint: srv.URL, ClientID: synapsetest.MASClientID, ClientSecret: synapsetest.MASClientSecret},
	})
	defer func() { _ = withMAS.Close() }()
	if _, err := withMAS.Probe(context.Background()); !isKind(err, adapter.Invalid) || !strings.Contains(err.Error(), "MAS") {
		t.Errorf("MAS credentials on Tuwunel: %v", err)
	}
}

// mas_secret removes the registration token routes and nothing else; the
// probe notices and the token operations answer NotSupported without a call.
func TestTuwunelProvisioningDropsRegistrationTokens(t *testing.T) {
	a, fake := startTuwunel(t)
	ctx := context.Background()
	fake.MASProvisioning(true)
	if _, err := a.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Capabilities().Has(adapter.CapMatrixRegTokens) || !a.Capabilities().Has(adapter.CapAccountsCreate) {
		t.Fatalf("capabilities = %v", a.Capabilities().Sorted())
	}
	pop := fake.Reset()
	adaptertest.CheckConsistency(t, ctx, a, pop)
	if _, err := a.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{Token: "t"}); !isKind(err, adapter.NotSupported) {
		t.Errorf("create token: %v", err)
	}
	if requested(fake, "POST /_synapse/admin/v1/registration_tokens/new") {
		t.Errorf("an undeclared operation reached the server: %v", fake.Requests)
	}
	fake.MASProvisioning(false)
	if _, err := a.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.Capabilities().Has(adapter.CapMatrixRegTokens) {
		t.Error("registration tokens did not come back")
	}
}

// The admin bit goes through the create-or-modify PUT, is checked in the
// reply, and is not sent at all when it already has the wanted value.
func TestTuwunelSetAdminUsesTheModifyPUT(t *testing.T) {
	a, fake := startTuwunel(t)
	ctx := context.Background()
	alice := "@alice:" + fakeDomain
	if err := a.SetAdmin(ctx, alice, true); err != nil {
		t.Fatalf("set admin: %v", err)
	}
	if got, err := a.GetAccount(ctx, alice); err != nil || !got.Admin {
		t.Errorf("admin not set: %+v, %v", got, err)
	}
	if !requested(fake, "PUT /_synapse/admin/v2/users/") {
		t.Errorf("requests = %v", fake.Requests)
	}
	for _, r := range fake.Requests {
		if strings.HasSuffix(r, "/admin") {
			t.Errorf("the unserved admin-bit route was requested: %s", r)
		}
	}
	puts := countRequests(fake, "PUT /_synapse/admin/v2/users/")
	if err := a.SetAdmin(ctx, alice, true); err != nil || countRequests(fake, "PUT /_synapse/admin/v2/users/") != puts {
		t.Errorf("a no-op grant was sent: %v %v", err, fake.Requests)
	}
	if err := a.SetAdmin(ctx, alice, false); err != nil {
		t.Fatalf("clear admin: %v", err)
	}
	if got, err := a.GetAccount(ctx, alice); err != nil || got.Admin {
		t.Errorf("admin not cleared: %+v, %v", got, err)
	}
	if err := a.SetAdmin(ctx, "@ghost:"+fakeDomain, true); !isKind(err, adapter.NotFound) {
		t.Errorf("set admin on a missing account: %v", err)
	}
	fake.NoAdminRoom(true)
	if err := a.SetAdmin(ctx, alice, true); !isKind(err, adapter.Upstream) || !strings.Contains(err.Error(), "admin room") {
		t.Errorf("grant without an admin room must not pass silently: %v", err)
	}
	fake.NoAdminRoom(false)
	created, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "fresh", Password: "fresh-pass-1"})
	if err != nil || !created.Enabled {
		t.Fatalf("create answered 200 was taken for a conflict: %+v, %v", created, err)
	}
}

// server_version is open to any token on both implementations, so the probe
// proves admin rights on the admin's own record instead.
func TestProbeRejectsANonAdminToken(t *testing.T) {
	for _, impl := range []adapter.Implementation{adapter.ImplSynapse, adapter.ImplTuwunel} {
		t.Run(string(impl), func(t *testing.T) {
			fake := synapsetest.New(fakeDomain, fakeToken)
			fake.Tuwunel(impl == adapter.ImplTuwunel)
			fake.SetHomeserverAdmin("@"+synapsetest.AdminLocalpart+":"+fakeDomain, false)
			srv := httptest.NewServer(fake)
			defer srv.Close()
			a := New(adapter.ServerConfig{
				Protocol: adapter.ProtocolMatrix, Impl: impl, Endpoint: srv.URL, Domain: fakeDomain,
				Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken},
			})
			defer func() { _ = a.Close() }()
			if _, err := a.Probe(context.Background()); !isKind(err, adapter.Forbidden) {
				t.Fatalf("probe with a non-admin token: %v", err)
			}
		})
	}
}
