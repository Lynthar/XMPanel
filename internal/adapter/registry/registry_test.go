package registry

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

// upstream is a minimal Prosody that answers the two probe requests and counts
// probes and TCP connections.
type upstream struct {
	srv         *httptest.Server
	token       atomic.Value
	status      atomic.Int64
	probes      atomic.Int64
	connections atomic.Int64
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.token.Store("initial-token")
	u.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := u.status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+u.token.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/admin_api/server/info":
			u.probes.Add(1)
			_, _ = w.Write([]byte(`{"version":"13.0.5","site_name":"example.com"}`))
		case "/admin_panel/sessions", "/admin_panel/users":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	u.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			u.connections.Add(1)
		}
	}
	u.srv.Start()
	t.Cleanup(u.srv.Close)
	return u
}

type fixture struct {
	db  *store.DB
	reg *Registry
	up  *upstream
	id  int64
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: storetest.NewDB(t), up: newUpstream(t), now: time.Now()}
	ring := storetest.NewKeyRing(t)
	f.id = storetest.InsertServer(t, f.db, ring, "prosody", f.up.srv.URL, "example.com", "initial-token")
	f.reg = New(f.db, ring, zap.NewNop())
	f.reg.now = func() time.Time { return f.now }
	t.Cleanup(f.reg.Close)
	return f
}

func TestGetProbesOnceAndReusesTheClient(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, info, err := f.reg.Get(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "13.0.5" || !a.Capabilities().Has(adapter.CapAccountsList) {
		t.Fatalf("info = %+v, caps = %v", info, a.Capabilities().Sorted())
	}
	if _, _, err := f.reg.Get(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListAccounts(ctx, adapter.ListQuery{}); err != nil {
		t.Fatal(err)
	}
	if f.up.probes.Load() != 1 {
		t.Fatalf("probed %d times, want 1", f.up.probes.Load())
	}
	if f.up.connections.Load() != 1 {
		t.Fatalf("used %d connections, want 1", f.up.connections.Load())
	}
}

func TestInvalidateReloadsCredentials(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, _, err := f.reg.Get(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	f.up.token.Store("replacement-token")
	if _, _, err := f.reg.Get(ctx, f.id); err != nil {
		t.Fatalf("cached client must keep working until invalidated: %v", err)
	}
	encrypted, err := store.EncryptCredentials(f.reg.keyRing, store.BearerCredentials("replacement-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE servers SET credentials_encrypted = $1 WHERE id = $2`, encrypted, f.id); err != nil {
		t.Fatal(err)
	}
	f.reg.Invalidate(f.id)
	a, _, err := f.reg.Get(ctx, f.id)
	if err != nil {
		t.Fatalf("after invalidate: %v", err)
	}
	if _, err := a.ListAccounts(ctx, adapter.ListQuery{}); err != nil {
		t.Fatalf("new token not in use: %v", err)
	}
	if f.up.probes.Load() != 2 {
		t.Fatalf("probed %d times, want 2", f.up.probes.Load())
	}
}

func TestProbeFailureIsCachedUntilTheRetryWindowPasses(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.up.status.Store(http.StatusUnauthorized)
	_, _, err := f.reg.Get(ctx, f.id)
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Unauthorized {
		t.Fatalf("first get: %v", err)
	}
	f.up.status.Store(0)
	f.now = f.now.Add(ProbeRetryAfter / 2)
	if _, _, err := f.reg.Get(ctx, f.id); err == nil {
		t.Fatal("failure was not served from cache inside the retry window")
	}
	if f.up.probes.Load() != 0 {
		t.Fatalf("upstream was contacted %d times inside the window", f.up.probes.Load())
	}
	f.now = f.now.Add(ProbeRetryAfter)
	if _, _, err := f.reg.Get(ctx, f.id); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestMissingRowIsNotCached(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, _, err := f.reg.Get(ctx, f.id+1000000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing row: %v", err)
	}
	f.reg.mu.Lock()
	_, cached := f.reg.entries[f.id+1000000]
	f.reg.mu.Unlock()
	if cached {
		t.Fatal("missing row left an entry behind")
	}
}

func TestConcurrentGetsShareOneProbe(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.reg.Get(context.Background(), f.id)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.up.probes.Load() != 1 {
		t.Fatalf("probed %d times under contention, want 1", f.up.probes.Load())
	}
}

func TestLookupCancellationDoesNotBlockCachedServers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, _, err := f.reg.Get(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`LOCK TABLE servers IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, _, err := f.reg.Get(blockedCtx, f.id+1000000)
		finished <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := f.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE relation = 'servers'::regclass AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lookup did not reach the database lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cachedCtx, cancelCached := context.WithTimeout(ctx, time.Second)
	defer cancelCached()
	if _, _, err := f.reg.Get(cachedCtx, f.id); err != nil {
		t.Fatalf("a blocked lookup blocked a cached server: %v", err)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lookup cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lookup ignored cancellation")
	}
}
