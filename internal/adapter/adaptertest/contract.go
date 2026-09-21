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

// Population describes what Reset leaves on the fake: bare account ids,
// live sessions (ID and AccountID) and room ids. Sessions must belong to
// accounts in Accounts; at least two of each populated kind are needed for
// the paging scenario.
type Population struct {
	Accounts []string
	Sessions []adapter.Session
	Rooms    []string
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
		for _, cap := range adapter.AllCapabilities {
			if cap == adapter.CapAccountsSearch {
				continue // server-side search is indistinguishable from local filtering from outside
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

	if c.Expected.Has(adapter.CapSessionsListAll) {
		t.Run("sessions", func(t *testing.T) { sessionScenario(t, ctx, c, fresh) })
	}

	if c.Expected.Has(adapter.CapRoomsList) {
		t.Run("rooms", func(t *testing.T) { roomScenario(t, ctx, c, fresh) })
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
		if acc.Localpart == "" || acc.Domain == "" || acc.Localpart+"@"+acc.Domain != acc.ID {
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
	if created.ID != "contract@"+c.Domain {
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
		if err := a.SetPassword(ctx, "ghost@"+c.Domain, "contract-pass-2"); !isKind(err, adapter.NotFound) {
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
	if _, err := a.GetAccount(ctx, "ghost@"+c.Domain); !isKind(err, adapter.NotFound) {
		t.Errorf("get missing account: %v", err)
	}
}

func sessionScenario(t *testing.T, ctx context.Context, c Config, fresh func(*testing.T) (adapter.Adapter, Population)) {
	a, pop := fresh(t)
	page, err := a.ListSessions(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := ids(pop.Sessions, func(s adapter.Session) string { return s.ID })
	if got := ids(page.Items, func(s adapter.Session) string { return s.ID }); !sameStrings(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
	for _, s := range page.Items {
		if s.AccountID == "" || !s.Live {
			t.Errorf("session %+v lacks account or liveness", s)
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
	page, err = a.ListSessions(ctx, adapter.ListQuery{Limit: adapter.MaxLimit})
	if err != nil {
		t.Fatalf("list after terminate: %v", err)
	}
	if contains(ids(page.Items, func(s adapter.Session) string { return s.ID }), first.ID) {
		t.Errorf("%s still listed after termination", first.ID)
	}
	if err := a.TerminateAccountSessions(ctx, pop.Sessions[1].AccountID); err != nil {
		t.Fatalf("terminate all: %v", err)
	}
	mine, err := a.ListAccountSessions(ctx, pop.Sessions[1].AccountID)
	if err != nil || len(mine) != 0 {
		t.Errorf("sessions remain after terminate all: %v %v", err, mine)
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
		if _, err := a.GetRoom(ctx, "ghost@"+c.Domain); !isKind(err, adapter.NotFound) {
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
	panic("unknown capability " + string(cap))
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
