//go:build smoke

// Package smoke drives each adapter against a real server started by
// smoke/docker-compose.yml. It is the only place a capability is proven
// against upstream rather than against a fake, so a capability an adapter
// declares must survive here before it may stay declared.
package smoke

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
	"github.com/xmpanel/xmpanel/internal/adapter/ejabberd"
	"github.com/xmpanel/xmpanel/internal/adapter/prosody"
)

const domain = "example.com"

type target struct {
	name     string
	impl     adapter.Implementation
	endpoint string
	c2s      string
	token    func(t *testing.T) string
	build    func(adapter.ServerConfig) adapter.Adapter
	rooms    bool
}

func compose(t *testing.T, args ...string) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.Join(dir, "docker-compose.yml")}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func targets() []target {
	return []target{
		{
			name: "prosody", impl: adapter.ImplProsody,
			endpoint: "http://127.0.0.1:15280", c2s: "127.0.0.1:15222",
			token: func(t *testing.T) string {
				return strings.TrimSpace(compose(t, "exec", "-T", "prosody", "cat", "/var/lib/prosody/admin-token.txt"))
			},
			build: func(cfg adapter.ServerConfig) adapter.Adapter { return prosody.New(cfg) },
		},
		{
			name: "ejabberd", impl: adapter.ImplEjabberd,
			endpoint: "http://127.0.0.1:15281", c2s: "127.0.0.1:15223",
			token: func(t *testing.T) string {
				out := compose(t, "exec", "-T", "ejabberd", "ejabberdctl", "oauth_issue_token", "admin@"+domain, "3600", "ejabberd:admin")
				// First whitespace-separated field of the first line is the token.
				return strings.Fields(strings.TrimSpace(out))[0]
			},
			build: func(cfg adapter.ServerConfig) adapter.Adapter { return ejabberd.New(cfg) },
			rooms: true,
		},
	}
}

// TestMain tears the servers down after the run unless XMPANEL_SMOKE_KEEP is
// set; os.Exit skips deferred calls, so the teardown runs before it.
func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv("XMPANEL_SMOKE_KEEP") == "" {
		_ = exec.Command("docker", "compose", "-f", "docker-compose.yml", "down", "-v", "--remove-orphans").Run()
	}
	os.Exit(code)
}

func TestRealServers(t *testing.T) {
	compose(t, "up", "-d", "--build", "--wait")
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) { run(t, tg) })
	}
}

func run(t *testing.T, tg target) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a := tg.build(adapter.ServerConfig{
		Protocol: adapter.ProtocolXMPP, Impl: tg.impl, Endpoint: tg.endpoint, Domain: domain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: tg.token(t)},
	})
	defer func() { _ = a.Close() }()

	info, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Impl != tg.impl || info.Version == "" || !contains(info.Domains, domain) {
		t.Fatalf("info = %+v", info)
	}
	caps := a.Capabilities()
	t.Logf("%s %s declares %v", tg.impl, info.Version, caps.Sorted())
	has := caps.Has

	stats, err := a.Stats(ctx)
	if err != nil || stats.RegisteredUsers == nil {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	// Accounts: create, duplicate, get, password, delete.
	const local = "smoke-user"
	id := local + "@" + domain
	_ = a.DeleteAccount(ctx, id) // leftovers from an aborted run
	created, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "first-password-1"})
	if err != nil || created.ID != id {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: local, Password: "first-password-1"}); !isKind(err, adapter.Conflict) {
		t.Errorf("duplicate create: %v", err)
	}
	if got, err := a.GetAccount(ctx, id); err != nil || got.ID != id || !got.Enabled {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if _, err := a.GetAccount(ctx, "ghost@"+domain); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing: %v", err)
	}
	if err := a.SetPassword(ctx, id, "second-password-2"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := a.SetPassword(ctx, "ghost@"+domain, "x-password-x"); !isKind(err, adapter.NotFound) {
		t.Errorf("set password on missing account: %v", err)
	}
	if _, err := connectXMPP(t, tg.c2s, domain, local, "first-password-1", "old"); err == nil {
		t.Errorf("old password still logs in")
	}
	client, err := connectXMPP(t, tg.c2s, domain, local, "second-password-2", "smoke")
	if err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	defer client.Close()

	// Sessions: the client's session must appear, be attributable, and be
	// closable from the adapter.
	page := waitFor(t, func() (adapter.Page[adapter.Session], bool) {
		page, err := a.ListSessions(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
		return page, sessionListed(page.Items, client.JID)
	})
	var mine adapter.Session
	for _, s := range page.Items {
		if s.ID == client.JID {
			mine = s
		}
	}
	if mine.AccountID != id || !mine.Live || mine.IP == "" {
		t.Errorf("session facts = %+v", mine)
	}
	own, err := a.ListAccountSessions(ctx, id)
	if err != nil || !sessionListed(own, client.JID) {
		t.Fatalf("account sessions = %+v, %v", own, err)
	}
	if err := a.TerminateSession(ctx, id, client.JID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !client.waitClosed(10 * time.Second) {
		t.Errorf("client connection survived termination")
	}
	waitFor(t, func() (string, bool) {
		own, err := a.ListAccountSessions(ctx, id)
		return fmt.Sprintf("%v %v", own, err), err == nil && !sessionListed(own, client.JID)
	})

	second, err := connectXMPP(t, tg.c2s, domain, local, "second-password-2", "again")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	defer second.Close()
	waitFor(t, func() (string, bool) {
		own, err := a.ListAccountSessions(ctx, id)
		return fmt.Sprintf("%v %v", own, err), err == nil && sessionListed(own, second.JID)
	})
	if err := a.TerminateAccountSessions(ctx, id); err != nil {
		t.Fatalf("terminate all: %v", err)
	}
	if !second.waitClosed(10 * time.Second) {
		t.Errorf("client survived terminate-all")
	}

	// Enable/disable is only claimed where a disabled account cannot log in.
	if has(adapter.CapAccountsSetEnabled) {
		if err := a.SetEnabled(ctx, id, false); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if got, err := a.GetAccount(ctx, id); err != nil || got.Enabled {
			t.Errorf("disabled account reads enabled: %+v, %v", got, err)
		}
		if c, err := connectXMPP(t, tg.c2s, domain, local, "second-password-2", "disabled"); err == nil {
			c.Close()
			t.Errorf("disabled account logged in")
		}
		if err := a.SetEnabled(ctx, id, true); err != nil {
			t.Fatalf("enable: %v", err)
		}
		if c, err := connectXMPP(t, tg.c2s, domain, local, "second-password-2", "enabled"); err != nil {
			t.Errorf("re-enabled account cannot log in: %v", err)
		} else {
			c.Close()
		}
	}

	if tg.rooms {
		service := "conference." + domain
		roomID := "smoke-room@" + service
		_ = a.DeleteRoom(ctx, roomID)
		room, err := a.CreateRoom(ctx, adapter.CreateRoom{Name: "smoke-room", Domain: service, Public: true, Persistent: true})
		if err != nil || room.ID != roomID {
			t.Fatalf("create room: %+v, %v", room, err)
		}
		listed, err := a.ListRooms(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil || !roomListed(listed.Items, roomID) {
			t.Fatalf("list rooms: %+v, %v", listed, err)
		}
		if got, err := a.GetRoom(ctx, roomID); err != nil || !got.Public || got.XMPP == nil || !got.XMPP.Persistent {
			t.Errorf("get room: %+v, %v", got, err)
		}
		if err := a.DeleteRoom(ctx, roomID); err != nil {
			t.Fatalf("delete room: %v", err)
		}
		if _, err := a.GetRoom(ctx, roomID); !isKind(err, adapter.NotFound) {
			t.Errorf("get deleted room: %v", err)
		}
	}

	if err := a.DeleteAccount(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := a.GetAccount(ctx, id); !isKind(err, adapter.NotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if c, err := connectXMPP(t, tg.c2s, domain, local, "second-password-2", "gone"); err == nil {
		c.Close()
		t.Errorf("deleted account still logs in")
	}

	// Every declared capability must work here and every undeclared one must
	// answer NotSupported. The population is a disposable account with one
	// live session, because the check mutates and finally deletes it.
	const disposable = "consistency-user"
	pop := adaptertest.Population{Accounts: []string{disposable + "@" + domain}}
	_ = a.DeleteAccount(ctx, pop.Accounts[0])
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: disposable, Password: "consistency-pass-1"}); err != nil {
		t.Fatalf("consistency account: %v", err)
	}
	defer func() { _ = a.DeleteAccount(ctx, pop.Accounts[0]) }()
	if tg.rooms {
		service := "conference." + domain
		pop.Rooms = []string{"consistency@" + service}
		_ = a.DeleteRoom(ctx, "consistency@"+service)
		_ = a.DeleteRoom(ctx, "invoke@"+service)
		if _, err := a.CreateRoom(ctx, adapter.CreateRoom{Name: "consistency", Domain: service}); err != nil {
			t.Fatalf("seed room: %v", err)
		}
		defer func() {
			_ = a.DeleteRoom(ctx, "consistency@"+service)
			_ = a.DeleteRoom(ctx, "invoke@"+service)
		}()
	}
	probe, err := connectXMPP(t, tg.c2s, domain, disposable, "consistency-pass-1", "consistency")
	if err != nil {
		t.Fatalf("consistency login: %v", err)
	}
	defer probe.Close()
	waitFor(t, func() (string, bool) {
		own, err := a.ListAccountSessions(ctx, pop.Accounts[0])
		return fmt.Sprintf("%v %v", own, err), err == nil && sessionListed(own, probe.JID)
	})
	pop.Sessions = []adapter.Session{{ID: probe.JID, AccountID: pop.Accounts[0]}}
	adaptertest.CheckConsistency(t, ctx, a, pop)
}

func waitFor[T any](t *testing.T, probe func() (T, bool)) T {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		v, ok := probe()
		if ok {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met in time: %+v", v)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func sessionListed(sessions []adapter.Session, jid string) bool {
	for _, s := range sessions {
		if s.ID == jid {
			return true
		}
	}
	return false
}

func roomListed(rooms []adapter.Room, id string) bool {
	for _, r := range rooms {
		if r.ID == id {
			return true
		}
	}
	return false
}

func contains(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

func isKind(err error, kind adapter.Kind) bool {
	failure, ok := adapter.AsError(err)
	return ok && failure.Kind == kind
}
