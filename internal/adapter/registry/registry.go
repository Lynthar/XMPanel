package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/ejabberd"
	"github.com/xmpanel/xmpanel/internal/adapter/prosody"
	"github.com/xmpanel/xmpanel/internal/adapter/synapse"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

type factory func(adapter.ServerConfig) adapter.Adapter

var factories = map[adapter.Implementation]factory{
	adapter.ImplProsody:  func(cfg adapter.ServerConfig) adapter.Adapter { return prosody.New(cfg) },
	adapter.ImplEjabberd: func(cfg adapter.ServerConfig) adapter.Adapter { return ejabberd.New(cfg) },
	adapter.ImplSynapse:  func(cfg adapter.ServerConfig) adapter.Adapter { return synapse.New(cfg) },
	adapter.ImplTuwunel:  func(cfg adapter.ServerConfig) adapter.Adapter { return synapse.New(cfg) },
}

var protocols = map[adapter.Implementation]adapter.Protocol{
	adapter.ImplProsody:  adapter.ProtocolXMPP,
	adapter.ImplEjabberd: adapter.ProtocolXMPP,
	adapter.ImplSynapse:  adapter.ProtocolMatrix,
	adapter.ImplTuwunel:  adapter.ProtocolMatrix,
}

// ProbeRetryAfter is how long a failed probe is answered from cache before
// the server is contacted again.
const ProbeRetryAfter = 30 * time.Second

var errInvalidated = errors.New("server configuration changed")
var errClosed = errors.New("adapter registry is closed")

type entry struct {
	ready    chan struct{}
	adapter  adapter.Adapter
	info     *adapter.ServerInfo
	err      error
	probedAt time.Time
}

// Registry owns adapter construction, the first Probe and cached clients.
// Entries become immutable when ready closes; a slow lookup never holds the
// registry lock.
type Registry struct {
	db      *store.DB
	keyRing *crypto.KeyRing
	logger  *zap.Logger
	now     func() time.Time
	mu      sync.Mutex
	entries map[int64]*entry
	closed  bool
}

func New(db *store.DB, keyRing *crypto.KeyRing, logger *zap.Logger) *Registry {
	return &Registry{db: db, keyRing: keyRing, logger: logger, now: time.Now, entries: make(map[int64]*entry)}
}

// Supports reports whether impl belongs to protocol and has a constructor.
func Supports(protocol adapter.Protocol, impl adapter.Implementation) bool {
	_, ok := factories[impl]
	return ok && protocols[impl] == protocol
}

// Get returns the probed adapter for a server. A probe failure is cached for
// ProbeRetryAfter so one unreachable server does not stall every request; a
// missing row or undecryptable credentials are never cached. A concurrent
// invalidation discards the old load and retries, so a stale row cannot be
// published. The request context bounds the lookup and the probe.
func (r *Registry) Get(ctx context.Context, id int64) (adapter.Adapter, *adapter.ServerInfo, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, nil, errClosed
		}
		item, found := r.entries[id]
		if found && r.expired(item) {
			delete(r.entries, id)
			found = false
		}
		if !found {
			item = &entry{ready: make(chan struct{})}
			r.entries[id] = item
		}
		r.mu.Unlock()
		if !found {
			r.load(ctx, id, item)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-item.ready:
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if errors.Is(item.err, errInvalidated) {
				continue
			}
			if found && (errors.Is(item.err, context.Canceled) || errors.Is(item.err, context.DeadlineExceeded)) {
				continue
			}
			if item.err != nil {
				return nil, nil, item.err
			}
			return item.adapter, item.info, nil
		}
	}
}

// Reprobe drops the cached entry and loads it again, for the operator's
// explicit connection test.
func (r *Registry) Reprobe(ctx context.Context, id int64) (adapter.Adapter, *adapter.ServerInfo, error) {
	r.Invalidate(id)
	return r.Get(ctx, id)
}

// expired is only true for a finished load whose probe failed long enough
// ago to try again; it must be called with r.mu held.
func (r *Registry) expired(item *entry) bool {
	select {
	case <-item.ready:
	default:
		return false
	}
	var failure *adapter.Error
	return errors.As(item.err, &failure) && r.now().Sub(item.probedAt) >= ProbeRetryAfter
}

func (r *Registry) load(ctx context.Context, id int64, item *entry) {
	a, info, err := r.construct(ctx, id)
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	r.mu.Lock()
	stale := r.entries[id] != item
	dropped := stale
	if stale {
		item.err = errInvalidated
	} else {
		item.adapter, item.info, item.err, item.probedAt = a, info, err, r.now()
		var failure *adapter.Error
		if err != nil && !errors.As(err, &failure) {
			delete(r.entries, id)
			item.adapter = nil
			dropped = true
		}
	}
	close(item.ready)
	r.mu.Unlock()
	if dropped {
		r.closeAdapter(a)
	}
}

func (r *Registry) construct(ctx context.Context, id int64) (adapter.Adapter, *adapter.ServerInfo, error) {
	var cfg adapter.ServerConfig
	var credentials sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, protocol, implementation, endpoint, domain, credentials_encrypted
		FROM servers WHERE id = $1
	`, id).Scan(&cfg.ID, &cfg.Protocol, &cfg.Impl, &cfg.Endpoint, &cfg.Domain, &credentials)
	if err != nil {
		return nil, nil, err
	}
	cfg.Creds, err = store.DecryptCredentials(r.keyRing, credentials.String)
	if err != nil {
		return nil, nil, err
	}
	factory, ok := factories[cfg.Impl]
	if !ok || protocols[cfg.Impl] != cfg.Protocol {
		return nil, nil, fmt.Errorf("unsupported server implementation: %s/%s", cfg.Protocol, cfg.Impl)
	}
	a := factory(cfg)
	info, err := a.Probe(ctx)
	if err != nil {
		return a, nil, err
	}
	return a, info, nil
}

// Invalidate must follow every successful server update or deletion. Removing
// a loading entry also prevents it from publishing a stale client when its
// query ends.
func (r *Registry) Invalidate(id int64) {
	r.mu.Lock()
	item := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	r.release(item)
}

func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	entries := r.entries
	r.entries = make(map[int64]*entry)
	r.mu.Unlock()
	for _, item := range entries {
		r.release(item)
	}
}

func (r *Registry) release(item *entry) {
	if item == nil {
		return
	}
	select {
	case <-item.ready:
		r.closeAdapter(item.adapter)
	default:
		// The loader owns cleanup until it has published its result.
	}
}

func (r *Registry) closeAdapter(a adapter.Adapter) {
	if a != nil {
		if err := a.Close(); err != nil {
			r.logger.Warn("failed to close adapter", zap.Error(err))
		}
	}
}
