//go:build smoke

package smoke

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/matrixgeneric"
)

// The registration token both containers accept (smoke/*/smoke.toml).
const registrationToken = "smoke-registration-token"

type genericTarget struct {
	name     string
	endpoint string
	token    func(t *testing.T) string
	whois    bool // the server serves whois, so connections are listed
}

// genericTargets are Continuwuity, which the panel only reaches this way,
// and Tuwunel driven through the spec alone to prove the whois path.
func genericTargets() []genericTarget {
	return []genericTarget{
		{
			name: "continuwuity", endpoint: "http://127.0.0.1:18011",
			token: func(t *testing.T) string {
				return strings.TrimSpace(compose(t, "exec", "-T", "continuwuity", "cat", "/var/lib/continuwuity/admin-token.txt"))
			},
		},
		{
			name: "tuwunel-generic", endpoint: "http://127.0.0.1:18010", whois: true,
			token: func(t *testing.T) string {
				return strings.TrimSpace(compose(t, "exec", "-T", "tuwunel", "cat", "/var/lib/tuwunel/admin-token.txt"))
			},
		},
	}
}

// runMatrixGeneric drives the spec-only adapter against a server the panel
// cannot list anything on: an account registered through the client API is
// looked up, locked (its client is refused), unlocked, suspended (it cannot
// send) and released; whois is declared only where the server serves it.
func runMatrixGeneric(t *testing.T, tg genericTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a := matrixgeneric.New(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: adapter.ImplMatrixGeneric, Endpoint: tg.endpoint, Domain: domain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: tg.token(t)},
	})
	defer func() { _ = a.Close() }()

	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Impl != adapter.ImplMatrixGeneric || info.Version == "" || !contains(info.Domains, domain) {
		t.Fatalf("info = %+v", info)
	}
	caps := a.Capabilities()
	t.Logf("%s %s declares %v", tg.name, info.Version, caps.Sorted())
	if !caps.Has(adapter.CapAccountsGet) || !caps.Has(adapter.CapAccountsSetEnabled) || !caps.Has(adapter.CapMatrixSuspend) || caps.Has(adapter.CapSessionsListByAcct) != tg.whois {
		t.Fatalf("capabilities = %v", caps.Sorted())
	}
	if stats, err := a.Stats(ctx); err != nil || stats.Version == "" {
		t.Errorf("stats = %+v, %v", stats, err)
	}

	suffix := strconv.FormatInt(time.Now().UnixMilli(), 36)
	local := "generic-" + suffix
	id := "@" + local + ":" + domain
	client, err := matrixRegister(tg.endpoint, local, "generic-pass-1", registrationToken, "smoke")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer client.logout()
	got, err := a.GetAccount(ctx, id)
	if err != nil || got.ID != id || !got.Enabled || got.Matrix == nil || got.Matrix.Locked || got.Matrix.Suspended == nil || *got.Matrix.Suspended {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := a.GetAccount(ctx, "@ghost-"+suffix+":"+domain); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing: %v", err)
	}
	if _, err := a.ListAccounts(ctx, adapter.ListQuery{}); !isKind(err, adapter.NotSupported) {
		t.Errorf("listing must be unsupported: %v", err)
	}

	// Lock: the client is refused and the account reads disabled.
	if err := a.SetEnabled(ctx, id, false); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := client.whoami(); err == nil {
		t.Errorf("locked account still served")
	} else {
		t.Logf("locked request refused: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.Enabled || !got.Matrix.Locked {
		t.Errorf("locked account reads %+v, %v", got, err)
	}
	if err := a.SetEnabled(ctx, id, true); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := client.whoami(); err != nil {
		t.Errorf("unlocked account refused: %v", err)
	}

	// Suspend: the account cannot create a room; release restores it.
	if err := a.SetSuspended(ctx, id, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := client.createRoom("while-suspended-"+suffix, false); err == nil {
		t.Errorf("suspended account could create a room")
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.Matrix.Suspended == nil || !*got.Matrix.Suspended {
		t.Errorf("suspended account reads %+v, %v", got, err)
	}
	if err := a.SetSuspended(ctx, id, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := client.createRoom("after-release-"+suffix, false); err != nil {
		t.Errorf("released account cannot create a room: %v", err)
	}
	if err := a.SetSuspended(ctx, "@ghost-"+suffix+":"+domain, true); !isKind(err, adapter.NotFound) {
		t.Errorf("suspend missing: %v", err)
	}

	// Connections: the registered client has been seen from one address.
	if tg.whois {
		sessions, err := a.ListAccountSessions(ctx, id)
		if err != nil || len(sessions) == 0 || sessions[0].AccountID != id || sessions[0].IP == "" || sessions[0].Live {
			t.Errorf("sessions = %+v, %v", sessions, err)
		}
		if _, err := a.ListAccountSessions(ctx, "@ghost-"+suffix+":"+domain); !isKind(err, adapter.NotFound) {
			t.Errorf("whois on a missing account: %v", err)
		}
	} else if _, err := a.ListAccountSessions(ctx, id); !isKind(err, adapter.NotSupported) {
		t.Errorf("sessions without whois: %v", err)
	}

	pop := adaptertest.Population{Accounts: []string{id}}
	adaptertest.CheckConsistency(t, ctx, a, pop)
}
