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

// The MAS admin client declared in smoke/mas/smoke.yaml.
const (
	masClientID     = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	masClientSecret = "smoke-mas-client-secret"
)

type matrixTarget struct {
	name      string
	impl      adapter.Implementation
	authMode  string
	endpoint  string
	loginBase string // where /login and /logout go: MAS under delegation
	creds     func(t *testing.T) adapter.Credentials
	build     func(adapter.ServerConfig) adapter.Adapter
}

func matrixTargets() []matrixTarget {
	build := func(cfg adapter.ServerConfig) adapter.Adapter { return synapse.New(cfg) }
	return []matrixTarget{
		{
			name: "synapse", impl: adapter.ImplSynapse, authMode: synapse.AuthModeLegacy,
			endpoint: "http://127.0.0.1:18008", loginBase: "http://127.0.0.1:18008",
			creds: func(t *testing.T) adapter.Credentials {
				token := strings.TrimSpace(compose(t, "exec", "-T", "synapse", "cat", "/var/lib/matrix-synapse/admin-token.txt"))
				return adapter.Credentials{Kind: adapter.CredentialsBearer, Token: token}
			},
			build: build,
		},
		{
			name: "synapse-mas", impl: adapter.ImplSynapse, authMode: synapse.AuthModeMAS,
			endpoint: "http://127.0.0.1:18009", loginBase: "http://127.0.0.1:18080",
			creds: func(t *testing.T) adapter.Credentials {
				token := strings.TrimSpace(compose(t, "exec", "-T", "mas", "cat", "/var/lib/mas/admin-token.txt"))
				return adapter.Credentials{
					Kind: adapter.CredentialsBearerMAS, Token: token,
					MAS: &adapter.MASCredentials{Endpoint: "http://127.0.0.1:18080", ClientID: masClientID, ClientSecret: masClientSecret},
				}
			},
			build: build,
		},
	}
}

// runMatrix is run for a Matrix backend: devices stand in for sessions,
// delete is deactivation, and a deactivated id stays taken forever, so every
// account this run creates carries a fresh suffix. Under MAS the account
// state reaches Synapse asynchronously, hence the waits after each change.
func runMatrix(t *testing.T, tg matrixTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := tg.build(adapter.ServerConfig{
		Protocol: adapter.ProtocolMatrix, Impl: tg.impl, Endpoint: tg.endpoint, Domain: domain, Creds: tg.creds(t),
	})
	defer func() { _ = a.Close() }()
	login := func(localpart, password, device string) (*matrixClient, error) {
		return matrixLogin(tg.loginBase, tg.endpoint, localpart, password, device)
	}

	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Impl != tg.impl || info.Version == "" || !contains(info.Domains, domain) || info.AuthMode != tg.authMode {
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
	got := waitFor(t, func() (*adapter.Account, bool) {
		got, err := a.GetAccount(ctx, id)
		return got, err == nil
	})
	if got.ID != id || !got.Enabled || got.DisplayName != "Smoke" {
		t.Fatalf("get: %+v", got)
	}
	if _, err := a.GetAccount(ctx, ghost); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing: %v", err)
	}
	first, err := login(local, "first-password-1", "first")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := a.SetPassword(ctx, id, "second-password-2"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := a.SetPassword(ctx, ghost, "x-password-x"); !isKind(err, adapter.NotFound) {
		t.Errorf("set password on missing account: %v", err)
	}
	if c, err := login(local, "first-password-1", "old"); err == nil {
		c.logout()
		t.Errorf("old password still logs in")
	}
	// A password change logs every device out on both pairs (MAS has no
	// switch for it, so the adapter revokes the devices itself).
	waitFor(t, func() (string, bool) {
		err := first.whoami()
		return fmt.Sprint(err), err != nil
	})

	// Devices: a login must appear under the account, be attributable, and
	// be revoked by the adapter.
	client, err := login(local, "second-password-2", "smoke")
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
	second, err := login(local, "second-password-2", "again")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	third, err := login(local, "second-password-2", "third")
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
	waitFor(t, func() (*adapter.Account, bool) {
		got, err := a.GetAccount(ctx, id)
		return got, err == nil && !got.Enabled && got.Matrix != nil && got.Matrix.Locked
	})
	if c, err := login(local, "second-password-2", "locked"); err == nil {
		c.logout()
		t.Errorf("locked account logged in")
	} else {
		t.Logf("locked login refused: %v", err)
	}
	waitFor(t, func() (adapter.Page[adapter.Account], bool) {
		page, err := a.ListAccounts(ctx, adapter.ListQuery{Search: local, Limit: adapter.MaxLimit})
		return page, err == nil && len(page.Items) == 1 && !page.Items[0].Enabled
	})
	if err := a.SetEnabled(ctx, id, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	unlocked, err := login(local, "second-password-2", "unlocked")
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
	if page, err := a.ListAccounts(ctx, adapter.ListQuery{Search: local, Limit: adapter.MaxLimit}); err != nil || len(page.Items) != 1 || !page.Items[0].Admin {
		t.Errorf("listing does not show the admin bit: %+v, %v", page, err)
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
	waitFor(t, func() (string, bool) {
		_, err := a.GetAccount(ctx, id)
		return fmt.Sprint(err), isKind(err, adapter.NotFound)
	})
	if c, err := login(local, "second-password-2", "gone"); err == nil {
		c.logout()
		t.Errorf("deactivated account still logs in")
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "again-3"}); !isKind(err, adapter.Conflict) {
		t.Errorf("recreating a deactivated id: %v", err)
	}
	waitFor(t, func() (adapter.Page[adapter.Account], bool) {
		page, err := a.ListAccounts(ctx, adapter.ListQuery{Search: local, Limit: adapter.MaxLimit})
		return page, err == nil && len(page.Items) == 0
	})

	runMatrixAdmin(t, ctx, a, login, suffix)

	// Every declared capability must work here and every undeclared one must
	// answer NotSupported, on a disposable account with one device and one room.
	disposable := "consistency-" + suffix
	pop := adaptertest.Population{Accounts: []string{"@" + disposable + ":" + domain}}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: disposable, Password: "consistency-pass-1"}); err != nil {
		t.Fatalf("consistency account: %v", err)
	}
	waitFor(t, func() (string, bool) {
		_, err := a.GetAccount(ctx, pop.Accounts[0])
		return fmt.Sprint(err), err == nil
	})
	probe, err := login(disposable, "consistency-pass-1", "consistency")
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
	pop.Media = mediaIDs(t, ctx, a, pop.Accounts[0])
	adaptertest.CheckConsistency(t, ctx, a, pop)
}

// mediaIDs lists what the account has uploaded, for the consistency population.
func mediaIDs(t *testing.T, ctx context.Context, a adapter.Adapter, account string) []string {
	t.Helper()
	m, ok := a.(adapter.MatrixAdmin)
	if !ok {
		return nil
	}
	page, err := m.ListAccountMedia(ctx, account, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("list media: %v", err)
	}
	ids := make([]string, len(page.Items))
	for i, md := range page.Items {
		ids[i] = md.ID
	}
	return ids
}

// runMatrixAdmin walks every MatrixAdmin capability against the real server
// with a fresh account: suspension bites and reads back, shadow ban reads
// back, tokens round-trip, an upload shows in media, a report is listed, a
// notice invites the recipient, block toggles, purge and erasure are final.
func runMatrixAdmin(t *testing.T, ctx context.Context, a adapter.Adapter, login func(string, string, string) (*matrixClient, error), suffix string) {
	m, ok := a.(adapter.MatrixAdmin)
	if !ok {
		t.Fatalf("%T is not a MatrixAdmin", a)
	}
	caps := a.Capabilities()
	local := "mod-" + suffix
	id := "@" + local + ":" + domain
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "moderated-pass-1"}); err != nil {
		t.Fatalf("moderation account: %v", err)
	}
	waitFor(t, func() (string, bool) {
		_, err := a.GetAccount(ctx, id)
		return fmt.Sprint(err), err == nil
	})
	client, err := login(local, "moderated-pass-1", "moderated")
	if err != nil {
		t.Fatalf("moderation login: %v", err)
	}
	defer client.logout()
	roomID, err := client.createRoom("moderated-"+suffix, false)
	if err != nil {
		t.Fatalf("moderation room: %v", err)
	}

	// Suspension: reads back, and the suspended account cannot send.
	if caps.Has(adapter.CapMatrixSuspend) {
		if err := m.SetSuspended(ctx, id, true); err != nil {
			t.Fatalf("suspend: %v", err)
		}
		if got, err := a.GetAccount(ctx, id); err != nil || got.Matrix == nil || got.Matrix.Suspended == nil || !*got.Matrix.Suspended {
			t.Errorf("suspended account reads %+v, %v", got, err)
		}
		if _, err := client.send(roomID, "while suspended"); err == nil {
			t.Errorf("suspended account could send")
		}
		if err := m.SetSuspended(ctx, id, false); err != nil {
			t.Fatalf("unsuspend: %v", err)
		}
		if _, err := client.send(roomID, "after unsuspend"); err != nil {
			t.Errorf("unsuspended account cannot send: %v", err)
		}
	}
	if caps.Has(adapter.CapMatrixShadowBan) {
		if err := m.SetShadowBanned(ctx, id, true); err != nil {
			t.Fatalf("shadow ban: %v", err)
		}
		if got, err := a.GetAccount(ctx, id); err != nil || got.Matrix == nil || !got.Matrix.ShadowBanned {
			t.Errorf("shadow-banned account reads %+v, %v", got, err)
		}
		if err := m.SetShadowBanned(ctx, id, false); err != nil {
			t.Fatalf("lift shadow ban: %v", err)
		}
	}

	// Registration tokens: create, list, delete/revoke, missing.
	if caps.Has(adapter.CapMatrixRegTokens) {
		uses := 2
		created, err := m.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{Token: "smoke-" + suffix, UsesAllowed: &uses})
		if err != nil || created.Token != "smoke-"+suffix || created.UsesAllowed == nil || *created.UsesAllowed != 2 || !created.Valid {
			t.Fatalf("create token: %+v, %v", created, err)
		}
		if _, err := m.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{Token: "smoke-" + suffix}); !isKind(err, adapter.Conflict) {
			t.Errorf("duplicate token: %v", err)
		}
		tokens, err := m.ListRegistrationTokens(ctx)
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		listed := false
		for _, tk := range tokens {
			listed = listed || tk.Token == created.Token
		}
		if !listed {
			t.Errorf("created token missing from %v", tokens)
		}
		if err := m.DeleteRegistrationToken(ctx, created.Token); err != nil {
			t.Fatalf("delete token: %v", err)
		}
		if err := m.DeleteRegistrationToken(ctx, "missing-"+suffix); !isKind(err, adapter.NotFound) {
			t.Errorf("delete missing token: %v", err)
		}
	}

	// Media: an upload is listed, quarantined and deleted.
	if caps.Has(adapter.CapMatrixMedia) {
		mediaID, err := client.upload("smoke.txt", []byte("smoke media "+suffix))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		page := waitFor(t, func() (adapter.Page[adapter.Media], bool) {
			page, err := m.ListAccountMedia(ctx, id, adapter.ListQuery{Limit: adapter.MaxLimit})
			if err != nil {
				t.Fatalf("list media: %v", err)
			}
			for _, md := range page.Items {
				if md.ID == mediaID {
					return page, true
				}
			}
			return page, false
		})
		for _, md := range page.Items {
			if md.ID == mediaID && (md.Name != "smoke.txt" || md.Size == 0 || md.CreatedAt == nil) {
				t.Errorf("media facts = %+v", md)
			}
		}
		if n, err := m.QuarantineAccountMedia(ctx, id); err != nil || n < 1 {
			t.Errorf("quarantine = %d, %v", n, err)
		}
		if page, err := m.ListAccountMedia(ctx, id, adapter.ListQuery{Limit: adapter.MaxLimit}); err != nil || len(page.Items) == 0 || !page.Items[0].Quarantined {
			t.Errorf("media after quarantine = %+v, %v", page, err)
		}
		if err := m.DeleteMedia(ctx, mediaID); err != nil {
			t.Fatalf("delete media: %v", err)
		}
		if err := m.DeleteMedia(ctx, mediaID); !isKind(err, adapter.NotFound) {
			t.Errorf("delete deleted media: %v", err)
		}
	}

	// Reports: the account reports its own message; the report is listed.
	if caps.Has(adapter.CapMatrixReports) {
		eventID, err := client.send(roomID, "reportable "+suffix)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if err := client.report(roomID, eventID, "smoke report "+suffix); err != nil {
			t.Fatalf("report: %v", err)
		}
		waitFor(t, func() (adapter.Page[adapter.EventReport], bool) {
			page, err := m.ListReports(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
			if err != nil {
				t.Fatalf("list reports: %v", err)
			}
			for _, r := range page.Items {
				if r.EventID == eventID && r.Reporter == id && r.Reason == "smoke report "+suffix && r.RoomID == roomID {
					return page, true
				}
			}
			return page, false
		})
	}

	// Server notice: the recipient is invited to a notices room, which then
	// shows up in the server's room listing under the configured name.
	if caps.Has(adapter.CapMatrixServerNotice) {
		if err := m.SendServerNotice(ctx, id, "smoke notice "+suffix); err != nil {
			t.Fatalf("server notice: %v", err)
		}
		waitFor(t, func() (adapter.Page[adapter.Room], bool) {
			page, err := a.ListRooms(ctx, adapter.ListQuery{Search: "Server Notices", Limit: adapter.MaxLimit})
			return page, err == nil && len(page.Items) > 0
		})
		invites := waitFor(t, func() ([]string, bool) {
			invites, err := client.invitedRooms()
			return invites, err == nil && len(invites) > 0
		})
		t.Logf("notice room invite: %v", invites)
		if err := m.SendServerNotice(ctx, "@ghost-"+suffix+":"+domain, "x"); !isKind(err, adapter.NotFound) {
			t.Errorf("notice to a missing account: %v", err)
		}
	}
	if caps.Has(adapter.CapMatrixFederation) {
		if _, err := m.ListFederationDestinations(ctx, adapter.ListQuery{Limit: adapter.MaxLimit}); err != nil {
			t.Errorf("list destinations: %v", err)
		}
	}

	// Block and unblock are accepted; whether joins are refused is not tested.
	if caps.Has(adapter.CapMatrixRoomBlock) {
		if err := m.BlockRoom(ctx, roomID, true); err != nil {
			t.Fatalf("block: %v", err)
		}
		if err := m.BlockRoom(ctx, roomID, false); err != nil {
			t.Fatalf("unblock: %v", err)
		}
	}
	// Purge with block: the room goes and stays gone.
	if caps.Has(adapter.CapMatrixRoomPurge) {
		deleteID, err := m.PurgeRoom(ctx, roomID, adapter.PurgeRoom{Purge: true, Block: true})
		if err != nil || deleteID == "" {
			t.Fatalf("purge: %q, %v", deleteID, err)
		}
		waitFor(t, func() (string, bool) {
			_, err := a.GetRoom(ctx, roomID)
			return fmt.Sprint(err), isKind(err, adapter.NotFound)
		})
		if _, err := m.PurgeRoom(ctx, "!ghost-"+suffix+":"+domain, adapter.PurgeRoom{Purge: true}); !isKind(err, adapter.NotFound) {
			t.Errorf("purge missing room: %v", err)
		}
	}

	// Erase: the account is gone, cannot log in, and the id stays taken.
	if caps.Has(adapter.CapMatrixDeactivate) {
		if err := m.Deactivate(ctx, id, true); err != nil {
			t.Fatalf("deactivate with erase: %v", err)
		}
		waitFor(t, func() (string, bool) {
			_, err := a.GetAccount(ctx, id)
			return fmt.Sprint(err), isKind(err, adapter.NotFound)
		})
		if c, err := login(local, "moderated-pass-1", "erased"); err == nil {
			c.logout()
			t.Errorf("erased account still logs in")
		}
		if err := m.Deactivate(ctx, "@ghost-"+suffix+":"+domain, false); !isKind(err, adapter.NotFound) {
			t.Errorf("deactivate missing account: %v", err)
		}
	}
}
