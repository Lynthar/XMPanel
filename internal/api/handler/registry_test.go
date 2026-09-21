package handler

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

type registryFixture struct {
	db          *store.DB
	adapters    *registry.Registry
	handler     *ServerHandler
	id          int64
	token       atomic.Value
	requests    atomic.Int64
	connections atomic.Int64
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	f := &registryFixture{db: newTestDB(t)}
	f.token.Store("initial-token")
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+f.token.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"version":"13.0.5","site_name":"example.com"}`))
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			f.connections.Add(1)
		}
	}
	upstream.Start()
	t.Cleanup(upstream.Close)
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := ring.EncryptString("initial-token")
	if err != nil {
		t.Fatal(err)
	}
	err = f.db.QueryRow(`INSERT INTO xmpp_servers (name, type, host, port, tls_enabled, api_key_encrypted)
        VALUES ('registry fixture', 'prosody', $1, $2, FALSE, $3) RETURNING id`, host, port, encrypted).Scan(&f.id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.db.Exec(`DELETE FROM xmpp_servers WHERE id = $1`, f.id); err != nil {
			t.Error(err)
		}
	})
	logger := zap.NewNop()
	f.adapters = registry.New(f.db, ring, logger)
	t.Cleanup(f.adapters.Close)
	f.handler = NewServerHandler(f.db, ring, f.adapters, nil, logger)
	return f
}

func (f *registryFixture) ping(t *testing.T) {
	t.Helper()
	a, err := f.adapters.Get(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerWritesInvalidateCachedCredentials(t *testing.T) {
	f := newRegistryFixture(t)
	if _, err := f.adapters.Get(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("constructing an adapter unexpectedly probed the upstream")
	}
	f.ping(t)
	f.ping(t)
	if f.connections.Load() != 1 {
		t.Fatalf("serial requests used %d connections", f.connections.Load())
	}
	f.token.Store("replacement-token")
	req := httptest.NewRequest("PUT", "/", strings.NewReader(`{"api_key":"replacement-token"}`))
	req.SetPathValue("id", strconv.FormatInt(f.id, 10))
	rec := httptest.NewRecorder()
	f.handler.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	f.ping(t)
	if f.connections.Load() != 2 {
		t.Fatalf("invalidated client was reused: %d connections", f.connections.Load())
	}

	req = httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", strconv.FormatInt(f.id, 10))
	rec = httptest.NewRecorder()
	f.handler.Delete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.adapters.Get(context.Background(), f.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted server remains available: %v", err)
	}
	f.adapters.Close()
	f.adapters.Close()
	if _, err := f.adapters.Get(context.Background(), f.id); err == nil {
		t.Fatal("closed registry accepted a lookup")
	}
}

func TestRegistryLookupCancellationDoesNotBlockCachedServers(t *testing.T) {
	f := newRegistryFixture(t)
	f.ping(t)
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`LOCK TABLE xmpp_servers IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := f.adapters.Get(ctx, f.id+1000000)
		finished <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		err := f.db.QueryRow(`SELECT EXISTS (
            SELECT 1 FROM pg_locks WHERE relation = 'xmpp_servers'::regclass AND NOT granted
        )`).Scan(&waiting)
		if err != nil {
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
	cachedContext, cancelCached := context.WithTimeout(context.Background(), time.Second)
	defer cancelCached()
	if _, err := f.adapters.Get(cachedContext, f.id); err != nil {
		t.Fatalf("one blocked lookup blocked a cached server: %v", err)
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
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var readers sync.WaitGroup
	failures := make(chan error, 24)
	for range 24 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			_, err := f.adapters.Get(context.Background(), f.id)
			failures <- err
		}()
	}
	readers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
}
