// Package adaptertest runs one behavioural contract against every adapter
// implementation, driving a fake upstream that speaks the implementation's
// own wire format. An implementation's test package supplies the fake and a
// constructor; the scenarios here are the same for all of them.
package adaptertest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Upstream is a fake server. Reset returns it to a known population; FailWith
// makes every following request answer with status until FailWith(0).
type Upstream interface {
	http.Handler
	Reset() Population
	FailWith(status int)
}

// Population describes what Reset leaves on the fake: account ids in the
// protocol's native form, sessions (ID and AccountID) and room ids. Sessions
// must belong to accounts in Accounts, the first two to different accounts;
// at least two of each populated kind are needed for the paging scenario.
// Media lists ids uploaded by Accounts[0], for the MatrixAdmin scenario.
type Population struct {
	Accounts []string
	Sessions []adapter.Session
	Rooms    []string
	Media    []string
}

type Config struct {
	Protocol adapter.Protocol
	Impl     adapter.Implementation
	Domain   string
	Upstream Upstream
	// New builds the adapter under test against the fake's base URL.
	New func(endpoint string) adapter.Adapter
	// Expected is the capability set the implementation declares once the
	// fake is fully featured; the contract checks it against behaviour.
	Expected adapter.CapabilitySet
}

// Run executes the contract. Every scenario starts from Upstream.Reset().
func Run(t *testing.T, c Config) {
	t.Helper()
	srv := httptest.NewServer(c.Upstream)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	fresh := func(t *testing.T) (adapter.Adapter, Population) {
		t.Helper()
		c.Upstream.FailWith(0)
		pop := c.Upstream.Reset()
		a := c.New(srv.URL)
		t.Cleanup(func() { _ = a.Close() })
		if _, err := a.Probe(ctx); err != nil {
			t.Fatalf("probe: %v", err)
		}
		return a, pop
	}

	t.Run("probe", func(t *testing.T) {
		c.Upstream.FailWith(0)
		c.Upstream.Reset()
		a := c.New(srv.URL)
		defer func() { _ = a.Close() }()
		info, err := a.Probe(ctx)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if info.Protocol != c.Protocol || info.Impl != c.Impl || info.Version == "" {
			t.Fatalf("info = %+v", info)
		}
		if !contains(info.Domains, c.Domain) {
			t.Fatalf("domains %v lack %s", info.Domains, c.Domain)
		}
		if got := a.Capabilities(); !sameSet(got, c.Expected) {
			t.Fatalf("capabilities = %v, want %v", got.Sorted(), c.Expected.Sorted())
		}
	})

	t.Run("capability consistency", func(t *testing.T) {
		a, pop := fresh(t)
		CheckConsistency(t, ctx, a, pop)
	})

	t.Run("stats", func(t *testing.T) {
		a, _ := fresh(t)
		stats, err := a.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats.Version == "" && stats.RegisteredUsers == nil && stats.OnlineUsers == nil {
			t.Fatal("stats carried nothing")
		}
	})

	if c.Expected.Has(adapter.CapAccountsGet) {
		t.Run("account lookup", func(t *testing.T) {
			a, pop := fresh(t)
			got, err := a.GetAccount(ctx, pop.Accounts[0])
			if err != nil || got.ID != pop.Accounts[0] || accountID(c.Protocol, got.Localpart, got.Domain) != got.ID {
				t.Fatalf("get %s: %+v, %v", pop.Accounts[0], got, err)
			}
			if _, err := a.GetAccount(ctx, accountID(c.Protocol, "ghost", c.Domain)); !isKind(err, adapter.NotFound) {
				t.Errorf("get missing account: %v", err)
			}
		})
	}

	if c.Expected.Has(adapter.CapAccountsList) {
		t.Run("accounts", func(t *testing.T) { accountScenario(t, ctx, c, fresh) })
		t.Run("account paging", func(t *testing.T) {
			a, pop := fresh(t)
			pageScenario(t, len(pop.Accounts), func(q adapter.ListQuery) ([]string, string, *int, error) {
				page, err := a.ListAccounts(ctx, q)
				ids := make([]string, len(page.Items))
				for i, acc := range page.Items {
					ids[i] = acc.ID
				}
				return ids, page.Next, page.Total, err
			})
		})
	}

	if c.Expected.Has(adapter.CapSessionsListAll) || c.Expected.Has(adapter.CapSessionsListByAcct) {
		t.Run("sessions", func(t *testing.T) { sessionScenario(t, ctx, c, fresh) })
	}

	if c.Expected.Has(adapter.CapRoomsList) {
		t.Run("rooms", func(t *testing.T) { roomScenario(t, ctx, c, fresh) })
	}

	if hasAny(c.Expected, adapter.MatrixCapabilities...) {
		t.Run("matrix admin", func(t *testing.T) { matrixScenario(t, ctx, c, fresh) })
	}

	t.Run("error classification", func(t *testing.T) {
		a, _ := fresh(t)
		for _, tc := range []struct {
			status int
			kind   adapter.Kind
		}{
			{http.StatusUnauthorized, adapter.Unauthorized},
			{http.StatusForbidden, adapter.Forbidden},
			{http.StatusInternalServerError, adapter.Upstream},
		} {
			c.Upstream.FailWith(tc.status)
			_, err := a.Probe(ctx)
			failure, ok := adapter.AsError(err)
			if !ok || failure.Kind != tc.kind || failure.Status != tc.status {
				t.Errorf("upstream %d: got %v (%+v), want kind %d", tc.status, err, failure, tc.kind)
			}
		}
		c.Upstream.FailWith(0)

		dead := c.New("http://127.0.0.1:1")
		defer func() { _ = dead.Close() }()
		_, err := dead.Probe(ctx)
		if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Unreachable {
			t.Errorf("unreachable endpoint: got %v", err)
		}

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = a.Probe(canceled)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled context lost its cause: %v", err)
		}
	})
}

func accountScenario(t *testing.T, ctx context.Context, c Config, fresh func(*testing.T) (adapter.Adapter, Population)) {
	a, pop := fresh(t)
	page, err := a.ListAccounts(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := ids(page.Items, func(acc adapter.Account) string { return acc.ID }); !sameStrings(got, pop.Accounts) {
		t.Fatalf("list = %v, want %v", got, pop.Accounts)
	}
	for _, acc := range page.Items {
		if acc.Localpart == "" || acc.Domain == "" || accountID(c.Protocol, acc.Localpart, acc.Domain) != acc.ID {
			t.Errorf("account %+v has inconsistent parts", acc)
		}
	}

	if !c.Expected.Has(adapter.CapAccountsCreate) {
		return
	}
	created, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "contract", Password: "contract-pass-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != accountID(c.Protocol, "contract", c.Domain) {
		t.Fatalf("created id = %q", created.ID)
	}
	if _, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "contract", Password: "contract-pass-1"}); !isKind(err, adapter.Conflict) {
		t.Errorf("duplicate create: %v", err)
	}
	got, err := a.GetAccount(ctx, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get after create: %v %+v", err, got)
	}
	if c.Expected.Has(adapter.CapAccountsSetPassword) {
		if err := a.SetPassword(ctx, created.ID, "contract-pass-2"); err != nil {
			t.Errorf("set password: %v", err)
		}
		if err := a.SetPassword(ctx, accountID(c.Protocol, "ghost", c.Domain), "contract-pass-2"); !isKind(err, adapter.NotFound) {
			t.Errorf("set password on missing account: %v", err)
		}
	}
	if c.Expected.Has(adapter.CapAccountsSetEnabled) {
		if err := a.SetEnabled(ctx, created.ID, false); err != nil {
			t.Errorf("disable: %v", err)
		}
		if got, err := a.GetAccount(ctx, created.ID); err != nil || got.Enabled {
			t.Errorf("disabled account reads enabled: %v %+v", err, got)
		}
		// A disabled account must stay listed, or it could never be re-enabled.
		listed, err := a.ListAccounts(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list while disabled: %v", err)
		}
		found := false
		for _, acc := range listed.Items {
			if acc.ID == created.ID {
				found = true
				if acc.Enabled {
					t.Errorf("listing shows the disabled account as enabled: %+v", acc)
				}
			}
		}
		if !found {
			t.Errorf("disabled account vanished from the listing")
		}
		if err := a.SetEnabled(ctx, created.ID, true); err != nil {
			t.Errorf("enable: %v", err)
		}
	}
	if c.Expected.Has(adapter.CapAccountsDelete) {
		if err := a.DeleteAccount(ctx, created.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := a.GetAccount(ctx, created.ID); !isKind(err, adapter.NotFound) {
			t.Errorf("get after delete: %v", err)
		}
	}
	if _, err := a.GetAccount(ctx, accountID(c.Protocol, "ghost", c.Domain)); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing account: %v", err)
	}
}

// sessionScenario goes through whichever listing the implementation
// declares: the global one, or only the per-account one where there is no
// global list (Synapse has no device listing across accounts).
func sessionScenario(t *testing.T, ctx context.Context, c Config, fresh func(*testing.T) (adapter.Adapter, Population)) {
	a, pop := fresh(t)
	listAll := c.Expected.Has(adapter.CapSessionsListAll)
	sessionID := func(s adapter.Session) string { return s.ID }
	listed := func(t *testing.T, want adapter.Session) bool {
		t.Helper()
		var sessions []adapter.Session
		var err error
		if listAll {
			var page adapter.Page[adapter.Session]
			page, err = a.ListSessions(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
			sessions = page.Items
		} else {
			sessions, err = a.ListAccountSessions(ctx, want.AccountID)
		}
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
		return contains(ids(sessions, sessionID), want.ID)
	}

	if listAll {
		page, err := a.ListSessions(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := ids(pop.Sessions, sessionID)
		if got := ids(page.Items, sessionID); !sameStrings(got, want) {
			t.Fatalf("list = %v, want %v", got, want)
		}
		// An XMPP session is a live connection; a Matrix device is not.
		for _, s := range page.Items {
			if s.AccountID == "" || (c.Protocol == adapter.ProtocolXMPP && !s.Live) {
				t.Errorf("session %+v lacks account or liveness", s)
			}
		}
	}

	first := pop.Sessions[0]
	if c.Expected.Has(adapter.CapSessionsListByAcct) {
		mine, err := a.ListAccountSessions(ctx, first.AccountID)
		if err != nil {
			t.Fatalf("list by account: %v", err)
		}
		for _, s := range mine {
			if s.AccountID != first.AccountID {
				t.Errorf("session %s belongs to %s, not %s", s.ID, s.AccountID, first.AccountID)
			}
		}
		if !contains(ids(mine, func(s adapter.Session) string { return s.ID }), first.ID) {
			t.Errorf("account listing lacks %s", first.ID)
		}
	}
	if !c.Expected.Has(adapter.CapSessionsTerminate) {
		return
	}
	if err := a.TerminateSession(ctx, first.AccountID, first.ID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if listed(t, first) {
		t.Errorf("%s still listed after termination", first.ID)
	}
	other := pop.Sessions[1]
	if err := a.TerminateAccountSessions(ctx, other.AccountID); err != nil {
		t.Fatalf("terminate all: %v", err)
	}
	for _, s := range pop.Sessions {
		if s.AccountID == other.AccountID && listed(t, s) {
			t.Errorf("%s remains after terminate all", s.ID)
		}
	}
}

func roomScenario(t *testing.T, ctx context.Context, c Config, fresh func(*testing.T) (adapter.Adapter, Population)) {
	a, pop := fresh(t)
	page, err := a.ListRooms(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := ids(page.Items, func(r adapter.Room) string { return r.ID }); !sameStrings(got, pop.Rooms) {
		t.Fatalf("list = %v, want %v", got, pop.Rooms)
	}
	if c.Expected.Has(adapter.CapRoomsGet) {
		room, err := a.GetRoom(ctx, pop.Rooms[0])
		if err != nil || room.ID != pop.Rooms[0] {
			t.Fatalf("get: %v %+v", err, room)
		}
		if _, err := a.GetRoom(ctx, missingRoomID(c)); !isKind(err, adapter.NotFound) {
			t.Errorf("get missing room: %v", err)
		}
	}
	if !c.Expected.Has(adapter.CapRoomsCreate) {
		return
	}
	_, service, _ := adapter.SplitJID(pop.Rooms[0])
	created, err := a.CreateRoom(ctx, adapter.CreateRoom{Name: "contract", Domain: service, Public: true, Persistent: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "contract@"+service {
		t.Fatalf("created id = %q", created.ID)
	}
	if c.Expected.Has(adapter.CapRoomsDelete) {
		if err := a.DeleteRoom(ctx, created.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := a.GetRoom(ctx, created.ID); !isKind(err, adapter.NotFound) {
			t.Errorf("get after delete: %v", err)
		}
	}
}

// matrixScenario exercises every declared MatrixAdmin capability against the
// population; the fake decides the shapes, the contract checks the effects.
func matrixScenario(t *testing.T, ctx context.Context, c Config, fresh func(*testing.T) (adapter.Adapter, Population)) {
	a, pop := fresh(t)
	m, ok := a.(adapter.MatrixAdmin)
	if !ok {
		t.Fatalf("%T declares matrix capabilities but is not a MatrixAdmin", a)
	}
	has := c.Expected.Has
	account := pop.Accounts[0]
	if len(pop.Accounts) > 1 {
		account = pop.Accounts[1]
	}

	if has(adapter.CapMatrixSuspend) {
		if err := m.SetSuspended(ctx, account, true); err != nil {
			t.Fatalf("suspend: %v", err)
		}
		if got, err := a.GetAccount(ctx, account); err != nil || got.Matrix == nil || got.Matrix.Suspended == nil || !*got.Matrix.Suspended {
			t.Errorf("suspended account reads %+v, %v", got, err)
		}
		if err := m.SetSuspended(ctx, account, false); err != nil {
			t.Errorf("unsuspend: %v", err)
		}
		if err := m.SetSuspended(ctx, accountID(c.Protocol, "ghost", c.Domain), true); !isKind(err, adapter.NotFound) {
			t.Errorf("suspend missing account: %v", err)
		}
	}
	if has(adapter.CapMatrixShadowBan) {
		if err := m.SetShadowBanned(ctx, account, true); err != nil {
			t.Fatalf("shadow ban: %v", err)
		}
		if got, err := a.GetAccount(ctx, account); err != nil || got.Matrix == nil || !got.Matrix.ShadowBanned {
			t.Errorf("shadow-banned account reads %+v, %v", got, err)
		}
		if err := m.SetShadowBanned(ctx, account, false); err != nil {
			t.Errorf("lift shadow ban: %v", err)
		}
		if err := m.SetShadowBanned(ctx, accountID(c.Protocol, "ghost", c.Domain), true); !isKind(err, adapter.NotFound) {
			t.Errorf("shadow ban missing account: %v", err)
		}
	}
	if has(adapter.CapMatrixRegTokens) {
		uses := 3
		created, err := m.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{UsesAllowed: &uses})
		if err != nil || created.Token == "" || created.UsesAllowed == nil || *created.UsesAllowed != 3 || !created.Valid {
			t.Fatalf("create token: %+v, %v", created, err)
		}
		named, err := m.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{Token: "contract-token"})
		if err != nil || named.Token != "contract-token" || named.UsesAllowed != nil {
			t.Fatalf("create named token: %+v, %v", named, err)
		}
		if _, err := m.CreateRegistrationToken(ctx, adapter.CreateRegistrationToken{Token: "contract-token"}); !isKind(err, adapter.Conflict) {
			t.Errorf("duplicate token: %v", err)
		}
		tokens, err := m.ListRegistrationTokens(ctx)
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		listed := map[string]adapter.RegistrationToken{}
		for _, tk := range tokens {
			listed[tk.Token] = tk
		}
		if _, ok := listed[created.Token]; !ok {
			t.Errorf("created token missing from %v", tokens)
		}
		if _, ok := listed["contract-token"]; !ok {
			t.Errorf("named token missing from %v", tokens)
		}
		if err := m.DeleteRegistrationToken(ctx, "contract-token"); err != nil {
			t.Fatalf("delete token: %v", err)
		}
		tokens, _ = m.ListRegistrationTokens(ctx)
		for _, tk := range tokens {
			if tk.Token == "contract-token" && tk.Valid {
				t.Errorf("deleted token still valid: %+v", tk)
			}
		}
		if err := m.DeleteRegistrationToken(ctx, "no-such-token"); !isKind(err, adapter.NotFound) {
			t.Errorf("delete missing token: %v", err)
		}
	}
	if has(adapter.CapMatrixReports) {
		page, err := m.ListReports(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list reports: %v", err)
		}
		for _, r := range page.Items {
			if r.ID == "" || r.RoomID == "" || r.EventID == "" || r.Reporter == "" {
				t.Errorf("report %+v lacks identity", r)
			}
		}
	}
	if has(adapter.CapMatrixMedia) {
		page, err := m.ListAccountMedia(ctx, pop.Accounts[0], adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list media: %v", err)
		}
		if got := ids(page.Items, func(md adapter.Media) string { return md.ID }); !sameStrings(got, pop.Media) {
			t.Fatalf("media = %v, want %v", got, pop.Media)
		}
		if len(pop.Media) > 0 && has(adapter.CapMatrixMediaQuarantine) {
			n, err := m.QuarantineAccountMedia(ctx, pop.Accounts[0])
			if err != nil || n != len(pop.Media) {
				t.Errorf("quarantine = %d, %v; want %d", n, err, len(pop.Media))
			}
			if _, err := m.QuarantineAccountMedia(ctx, accountID(c.Protocol, "ghost", c.Domain)); !isKind(err, adapter.NotFound) {
				t.Errorf("quarantine missing account: %v", err)
			}
			page, _ = m.ListAccountMedia(ctx, pop.Accounts[0], adapter.ListQuery{Limit: adapter.MaxLimit})
			for _, md := range page.Items {
				if !md.Quarantined {
					t.Errorf("media %s not quarantined", md.ID)
				}
			}
		}
		if len(pop.Media) > 0 {
			if err := m.DeleteMedia(ctx, pop.Media[0]); err != nil {
				t.Fatalf("delete media: %v", err)
			}
			page, _ = m.ListAccountMedia(ctx, pop.Accounts[0], adapter.ListQuery{Limit: adapter.MaxLimit})
			if contains(ids(page.Items, func(md adapter.Media) string { return md.ID }), pop.Media[0]) {
				t.Errorf("deleted media still listed")
			}
		}
		if err := m.DeleteMedia(ctx, "no-such-media"); !isKind(err, adapter.NotFound) {
			t.Errorf("delete missing media: %v", err)
		}
	}
	if has(adapter.CapMatrixRoomBlock) && len(pop.Rooms) > 0 {
		if err := m.BlockRoom(ctx, pop.Rooms[0], true); err != nil {
			t.Fatalf("block room: %v", err)
		}
		if err := m.BlockRoom(ctx, pop.Rooms[0], false); err != nil {
			t.Errorf("unblock room: %v", err)
		}
	}
	if has(adapter.CapMatrixServerNotice) {
		if err := m.SendServerNotice(ctx, account, "contract notice"); err != nil {
			t.Errorf("server notice: %v", err)
		}
		if err := m.SendServerNotice(ctx, accountID(c.Protocol, "ghost", c.Domain), "x"); !isKind(err, adapter.NotFound) {
			t.Errorf("notice to missing account: %v", err)
		}
	}
	if has(adapter.CapMatrixFederation) {
		page, err := m.ListFederationDestinations(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
		if err != nil {
			t.Fatalf("list destinations: %v", err)
		}
		for _, d := range page.Items {
			if d.Destination == "" {
				t.Errorf("destination without a name: %+v", d)
			}
		}
	}
	if has(adapter.CapMatrixRoomPurge) && len(pop.Rooms) > 1 {
		deleteID, err := m.PurgeRoom(ctx, pop.Rooms[1], adapter.PurgeRoom{Purge: true, Block: true})
		if err != nil || deleteID == "" {
			t.Fatalf("purge room: %q, %v", deleteID, err)
		}
		if _, err := m.PurgeRoom(ctx, missingRoomID(c), adapter.PurgeRoom{Purge: true}); !isKind(err, adapter.NotFound) {
			t.Errorf("purge missing room: %v", err)
		}
	}
	if has(adapter.CapMatrixDeactivate) {
		if err := m.Deactivate(ctx, account, true); err != nil {
			t.Fatalf("deactivate with erase: %v", err)
		}
		if _, err := a.GetAccount(ctx, account); !isKind(err, adapter.NotFound) {
			t.Errorf("get after erase: %v", err)
		}
		if err := m.Deactivate(ctx, accountID(c.Protocol, "ghost", c.Domain), false); !isKind(err, adapter.NotFound) {
			t.Errorf("deactivate missing account: %v", err)
		}
	}
}

func hasAny(set adapter.CapabilitySet, caps ...adapter.Capability) bool {
	for _, c := range caps {
		if set.Has(c) {
			return true
		}
	}
	return false
}

// pageScenario walks a listing one item at a time and checks the pages tile
// the full set exactly once.
func pageScenario(t *testing.T, total int, list func(adapter.ListQuery) ([]string, string, *int, error)) {
	if total < 2 {
		t.Skip("population too small to page")
	}
	all, _, _, err := list(adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("full list: %v", err)
	}
	var walked []string
	cursor := ""
	for pages := 0; ; pages++ {
		items, next, reported, err := list(adapter.ListQuery{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if reported != nil && *reported != total {
			t.Errorf("page %d reports total %d, want %d", pages, *reported, total)
		}
		walked = append(walked, items...)
		if next == "" {
			break
		}
		if pages > total {
			t.Fatalf("paging never ends")
		}
		cursor = next
	}
	if !sameStrings(walked, all) {
		t.Errorf("paged %v, full %v", walked, all)
	}
	if _, _, _, err := list(adapter.ListQuery{Limit: 1, Cursor: "not-a-cursor"}); !isKind(err, adapter.Invalid) {
		t.Errorf("bad cursor: %v", err)
	}
}

// CheckConsistency invokes every capability's operation and fails when a
// declared one answers NotSupported or an undeclared one answers anything
// else. Search is skipped: server-side search cannot be told from local
// filtering from outside.
func CheckConsistency(t *testing.T, ctx context.Context, a adapter.Adapter, pop Population) {
	t.Helper()
	for _, cap := range adapter.AllCapabilities {
		if cap == adapter.CapAccountsSearch {
			continue
		}
		err := invoke(ctx, a, cap, pop)
		failure, _ := adapter.AsError(err)
		unsupported := failure != nil && failure.Kind == adapter.NotSupported
		if a.Capabilities().Has(cap) && unsupported {
			t.Errorf("%s is declared but answered NotSupported", cap)
		}
		if !a.Capabilities().Has(cap) && !unsupported {
			t.Errorf("%s is not declared but did not answer NotSupported: %v", cap, err)
		}
	}
}

// invoke performs the operation a capability names, with arguments valid for
// the population, so NotSupported is the only error that means "undeclared".
func invoke(ctx context.Context, a adapter.Adapter, cap adapter.Capability, pop Population) error {
	account := ""
	if len(pop.Accounts) > 0 {
		account = pop.Accounts[0]
	}
	room := ""
	if len(pop.Rooms) > 0 {
		room = pop.Rooms[0]
	}
	switch cap {
	case adapter.CapAccountsList:
		_, err := a.ListAccounts(ctx, adapter.ListQuery{})
		return err
	case adapter.CapAccountsGet:
		_, err := a.GetAccount(ctx, account)
		return err
	case adapter.CapAccountsCreate:
		_, err := a.CreateAccount(ctx, adapter.CreateAccount{Localpart: "invoke", Password: "invoke-pass-1"})
		return err
	case adapter.CapAccountsDelete:
		return a.DeleteAccount(ctx, account)
	case adapter.CapAccountsSetPassword:
		return a.SetPassword(ctx, account, "invoke-pass-2")
	case adapter.CapAccountsSetEnabled:
		return a.SetEnabled(ctx, account, true)
	case adapter.CapAccountsSetAdmin:
		return a.SetAdmin(ctx, account, false)
	case adapter.CapSessionsListAll:
		_, err := a.ListSessions(ctx, adapter.ListQuery{})
		return err
	case adapter.CapSessionsListByAcct:
		_, err := a.ListAccountSessions(ctx, account)
		return err
	case adapter.CapSessionsTerminate:
		if len(pop.Sessions) == 0 {
			return a.TerminateAccountSessions(ctx, account)
		}
		return a.TerminateSession(ctx, pop.Sessions[0].AccountID, pop.Sessions[0].ID)
	case adapter.CapRoomsList:
		_, err := a.ListRooms(ctx, adapter.ListQuery{})
		return err
	case adapter.CapRoomsGet:
		_, err := a.GetRoom(ctx, room)
		return err
	case adapter.CapRoomsCreate:
		_, service, _ := adapter.SplitJID(room)
		_, err := a.CreateRoom(ctx, adapter.CreateRoom{Name: "invoke", Domain: service})
		return err
	case adapter.CapRoomsDelete:
		return a.DeleteRoom(ctx, room)
	}
	m, ok := a.(adapter.MatrixAdmin)
	if !ok {
		// Without the extension every matrix capability is unsupported.
		return adapter.NotSupportedError(string(cap))
	}
	media := ""
	if len(pop.Media) > 0 {
		media = pop.Media[0]
	}
	switch cap {
	case adapter.CapMatrixDeactivate:
		return m.Deactivate(ctx, account, false)
	case adapter.CapMatrixSuspend:
		return m.SetSuspended(ctx, account, false)
	case adapter.CapMatrixShadowBan:
		return m.SetShadowBanned(ctx, account, false)
	case adapter.CapMatrixRegTokens:
		_, err := m.ListRegistrationTokens(ctx)
		return err
	case adapter.CapMatrixReports:
		_, err := m.ListReports(ctx, adapter.ListQuery{})
		return err
	case adapter.CapMatrixMedia:
		_, err := m.ListAccountMedia(ctx, account, adapter.ListQuery{})
		if err == nil && media != "" {
			err = m.DeleteMedia(ctx, media)
		}
		return err
	case adapter.CapMatrixMediaQuarantine:
		_, err := m.QuarantineAccountMedia(ctx, account)
		return err
	case adapter.CapMatrixRoomBlock:
		return m.BlockRoom(ctx, room, false)
	case adapter.CapMatrixRoomPurge:
		_, err := m.PurgeRoom(ctx, room, adapter.PurgeRoom{Purge: true})
		return err
	case adapter.CapMatrixServerNotice:
		return m.SendServerNotice(ctx, account, "consistency check")
	case adapter.CapMatrixFederation:
		_, err := m.ListFederationDestinations(ctx, adapter.ListQuery{})
		return err
	}
	panic("unknown capability " + string(cap))
}

// accountID composes the protocol's native id for a localpart on domain.
func accountID(protocol adapter.Protocol, localpart, domain string) string {
	if protocol == adapter.ProtocolMatrix {
		return "@" + localpart + ":" + domain
	}
	return localpart + "@" + domain
}

// missingRoomID is a well-formed room id no fake populates.
func missingRoomID(c Config) string {
	if c.Protocol == adapter.ProtocolMatrix {
		return "!ghost:" + c.Domain
	}
	return "ghost@" + c.Domain
}

func isKind(err error, kind adapter.Kind) bool {
	failure, ok := adapter.AsError(err)
	return ok && failure.Kind == kind
}

func ids[T any](items []T, id func(T) string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = id(item)
	}
	return out
}

func contains(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func sameSet(a, b adapter.CapabilitySet) bool {
	if len(a) != len(b) {
		return false
	}
	for c := range a {
		if !b.Has(c) {
			return false
		}
	}
	return true
}
