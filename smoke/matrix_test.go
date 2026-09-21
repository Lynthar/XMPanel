//go:build smoke

package smoke

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/synapse"
)

type matrixTarget struct {
	name     string
	impl     adapter.Implementation
	endpoint string
	token    func(t *testing.T) string
	build    func(adapter.ServerConfig) adapter.Adapter
}

func matrixTargets() []matrixTarget {
	return []matrixTarget{
		{
			name: "synapse", impl: adapter.ImplSynapse, endpoint: "http://127.0.0.1:18008",
			token: func(t *testing.T) string {
				return strings.TrimSpace(compose(t, "exec", "-T", "synapse", "cat", "/var/lib/matrix-synapse/admin-token.txt"))
			},
			build: func(cfg adapter.ServerConfig) adapter.Adapter { return synapse.New(cfg) },
		},
	}
}

// runMatrix is run for a Matrix backend: devices stand in for sessions,
// delete is deactivation, and a deactivated id stays taken forever, so every
// account this run creates carries a fresh suffix.
func runMatrix(t *testing.T, tg matrixTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := tg.build(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: tg.impl, Endpoint: tg.endpoint, Domain: domain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: tg.token(t)},
	})
	defer func() { _ = a.Close() }()

	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Impl != tg.impl || info.Version == "" || !contains(info.Domains, domain) || info.AuthMode != synapse.AuthModeLegacy {
		t.Fatalf("info = %+v", info)
	}
	caps := a.Capabilities()
	t.Logf("%s %s declares %v", tg.impl, info.Version, caps.Sorted())

	stats, err := a.Stats(ctx)
	if err != nil || stats.RegisteredUsers == nil || stats.Rooms == nil {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	suffix := strconv.FormatInt(time.Now().UnixMilli(), 36)
	local := "smoke-" + suffix
	id := "@" + local + ":" + domain
	ghost := "@ghost-" + suffix + ":" + domain

	// Accounts: create, duplicate, get, password.
	created, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "first-password-1", DisplayName: "Smoke"})
	if err != nil || created.ID != id || !created.Enabled || created.Admin {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "first-password-1"}); !isKind(err, adapter.Conflict) {
		t.Errorf("duplicate create: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.ID != id || !got.Enabled || got.DisplayName != "Smoke" {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if _, err := a.GetAccount(ctx, ghost); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing: %v", err)
	}
	first, err := matrixLogin(tg.endpoint, local, "first-password-1", "first")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// A password change logs every device out, like the panel's own.
	if err := a.SetPassword(ctx, id, "second-password-2"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := a.SetPassword(ctx, ghost, "x-password-x"); !isKind(err, adapter.NotFound) {
		t.Errorf("set password on missing account: %v", err)
	}
	if c, err := matrixLogin(tg.endpoint, local, "first-password-1", "old"); err == nil {
		c.logout()
		t.Errorf("old password still logs in")
	}
	if err := first.whoami(); err == nil {
		t.Errorf("device survived the password change")
	}

	// Devices: a login must appear under the account, be attributable, and
	// be revoked by the adapter.
	client, err := matrixLogin(tg.endpoint, local, "second-password-2", "smoke")
	if err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	own, err := a.ListAccountSessions(ctx, id)
	if err != nil || !sessionListed(own, client.DeviceID) {
		t.Fatalf("account devices = %+v, %v", own, err)
	}
	for _, s := range own {
		if s.AccountID != id || s.Live {
			t.Errorf("device facts = %+v", s)
		}
	}
	if _, err := a.ListSessions(ctx, adapter.ListQuery{}); !isKind(err, adapter.NotSupported) {
		t.Errorf("global device list: %v", err)
	}
	if err := a.TerminateSession(ctx, id, client.DeviceID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if err := client.whoami(); err == nil {
		t.Errorf("device token survived termination")
	}
	waitFor(t, func() (string, bool) {
		own, err := a.ListAccountSessions(ctx, id)
		return fmt.Sprintf("%v %v", own, err), err == nil && !sessionListed(own, client.DeviceID)
	})
	second, err := matrixLogin(tg.endpoint, local, "second-password-2", "again")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	third, err := matrixLogin(tg.endpoint, local, "second-password-2", "third")
	if err != nil {
		t.Fatalf("third login: %v", err)
	}
	if err := a.TerminateAccountSessions(ctx, id); err != nil {
		t.Fatalf("terminate all: %v", err)
	}
	if second.whoami() == nil || third.whoami() == nil {
		t.Errorf("a device survived terminate-all")
	}
	if own, err := a.ListAccountSessions(ctx, id); err != nil || len(own) != 0 {
		t.Errorf("devices remain after terminate-all: %+v, %v", own, err)
	}

	// Lock: a locked account cannot log in; unlocking restores it.
	if err := a.SetEnabled(ctx, id, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.Enabled || got.Matrix == nil || !got.Matrix.Locked {
		t.Errorf("locked account = %+v, %v", got, err)
	}
	if c, err := matrixLogin(tg.endpoint, local, "second-password-2", "locked"); err == nil {
		c.logout()
		t.Errorf("locked account logged in")
	} else {
		t.Logf("locked login refused: %v", err)
	}
	if page, err := a.ListAccounts(ctx, adapter.ListQuery{Search: local, Limit: adapter.MaxLimit}); err != nil || len(page.Items) != 1 || page.Items[0].Enabled {
		t.Errorf("locked account must stay listed as disabled: %+v, %v", page, err)
	}
	if err := a.SetEnabled(ctx, id, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	unlocked, err := matrixLogin(tg.endpoint, local, "second-password-2", "unlocked")
	if err != nil {
		t.Fatalf("unlocked account cannot log in: %v", err)
	}

	// Admin bit.
	if err := a.SetAdmin(ctx, id, true); err != nil {
		t.Fatalf("set admin: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || !got.Admin {
		t.Errorf("admin not set: %+v, %v", got, err)
	}
	if err := a.SetAdmin(ctx, id, false); err != nil {
		t.Fatalf("clear admin: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.Admin {
		t.Errorf("admin not cleared: %+v, %v", got, err)
	}

	// Rooms: the account creates one through the client API; the adapter
	// lists, reads and purges it. Room stats and the purge are asynchronous.
	roomName := "smoke-room-" + suffix
	roomID, err := unlocked.createRoom(roomName, true)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	waitFor(t, func() (adapter.Page[adapter.Room], bool) {
		page, err := a.ListRooms(ctx, adapter.ListQuery{Search: roomName, Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list rooms: %v", err)
		}
		return page, roomListed(page.Items, roomID)
	})
	room := waitFor(t, func() (*adapter.Room, bool) {
		room, err := a.GetRoom(ctx, roomID)
		if err != nil {
			t.Fatalf("get room: %v", err)
		}
		return room, room.Members == 1
	})
	if room.Name != roomName || !room.Public || room.Matrix == nil || room.Matrix.Creator != id || room.Matrix.Version == "" {
		t.Errorf("room = %+v facts = %+v", room, room.Matrix)
	}
	ghostRoom := "!ghost-" + suffix + ":" + domain
	if _, err := a.GetRoom(ctx, ghostRoom); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing room: %v", err)
	}
	if err := a.DeleteRoom(ctx, ghostRoom); !isKind(err, adapter.NotFound) {
		t.Errorf("delete missing room: %v", err)
	}
	if err := a.DeleteRoom(ctx, roomID); err != nil {
		t.Fatalf("delete room: %v", err)
	}
	waitFor(t, func() (string, bool) {
		_, err := a.GetRoom(ctx, roomID)
		return fmt.Sprint(err), isKind(err, adapter.NotFound)
	})
	unlocked.logout()

	// Delete is deactivation: gone from the panel's point of view, id still taken.
	if err := a.DeleteAccount(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := a.GetAccount(ctx, id); !isKind(err, adapter.NotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if c, err := matrixLogin(tg.endpoint, local, "second-password-2", "gone"); err == nil {
		c.logout()
		t.Errorf("deactivated account still logs in")
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "again-3"}); !isKind(err, adapter.Conflict) {
		t.Errorf("recreating a deactivated id: %v", err)
	}
	if page, err := a.ListAccounts(ctx, adapter.ListQuery{Search: local, Limit: adapter.MaxLimit}); err != nil || len(page.Items) != 0 {
		t.Errorf("deactivated account still listed: %+v, %v", page, err)
	}

	// Every declared capability must work here and every undeclared one must
	// answer NotSupported, on a disposable account with one device and one room.
	disposable := "consistency-" + suffix
	pop := adaptertest.Population{Accounts: []string{"@" + disposable + ":" + domain}}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: disposable, Password: "consistency-pass-1"}); err != nil {
		t.Fatalf("consistency account: %v", err)
	}
	probe, err := matrixLogin(tg.endpoint, disposable, "consistency-pass-1", "consistency")
	if err != nil {
		t.Fatalf("consistency login: %v", err)
	}
	defer probe.logout()
	seed, err := probe.createRoom("consistency-"+suffix, false)
	if err != nil {
		t.Fatalf("seed room: %v", err)
	}
	pop.Rooms = []string{seed}
	pop.Sessions = []adapter.Session{{ID: probe.DeviceID, AccountID: pop.Accounts[0]}}
	adaptertest.CheckConsistency(t, ctx, a, pop)
}
