package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/ejabberd"
	"github.com/xmpanel/xmpanel/internal/adapter/prosody"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

var factories = map[models.ServerType]func(*models.XMPPServer, string) adapter.XMPPAdapter{
	models.ServerTypeProsody: func(server *models.XMPPServer, key string) adapter.XMPPAdapter {
		return prosody.NewAdapter(server, key)
	},
	models.ServerTypeEjabberd: func(server *models.XMPPServer, key string) adapter.XMPPAdapter {
		return ejabberd.NewAdapter(server, key)
	},
}

var errInvalidated = errors.New("server configuration changed")
var errClosed = errors.New("adapter registry is closed")

type entry struct {
	ready   chan struct{}
	adapter adapter.XMPPAdapter
	err     error
}

// Registry owns adapter construction and cached clients. Entries become
// immutable when ready closes; a slow lookup never holds the registry lock.
type Registry struct {
	db      *store.DB
	keyRing *crypto.KeyRing
	logger  *zap.Logger
	mu      sync.Mutex
	entries map[int64]*entry
	closed  bool
}

func New(db *store.DB, keyRing *crypto.KeyRing, logger *zap.Logger) *Registry {
	return &Registry{db: db, keyRing: keyRing, logger: logger, entries: make(map[int64]*entry)}
}

func Supports(kind models.ServerType) bool {
	_, ok := factories[kind]
	return ok
}

// Get reuses one client per server. A concurrent invalidation discards the
// old load and retries, so it cannot republish credentials from an older row.
// The request context bounds both the database lookup and waiting for a load.
func (r *Registry) Get(ctx context.Context, id int64) (adapter.XMPPAdapter, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, errClosed
		}
		item, found := r.entries[id]
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
			return nil, ctx.Err()
		case <-item.ready:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if errors.Is(item.err, errInvalidated) {
				continue
			}
			if found && (errors.Is(item.err, context.Canceled) || errors.Is(item.err, context.DeadlineExceeded)) {
				continue
			}
			return item.adapter, item.err
		}
	}
}

func (r *Registry) load(ctx context.Context, id int64, item *entry) {
	a, err := r.construct(ctx, id)
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	r.mu.Lock()
	stale := r.entries[id] != item
	if stale {
		item.err = errInvalidated
	} else {
		item.adapter, item.err = a, err
		if err != nil {
			delete(r.entries, id)
		}
	}
	close(item.ready)
	r.mu.Unlock()
	if stale {
		r.disconnect(a)
	}
}

func (r *Registry) construct(ctx context.Context, id int64) (adapter.XMPPAdapter, error) {
	var server models.XMPPServer
	var encryptedAPIKey sql.NullString
	err := r.db.QueryRowContext(ctx, `
        SELECT id, name, type, host, port, api_key_encrypted, tls_enabled, enabled
        FROM xmpp_servers WHERE id = $1
    `, id).Scan(&server.ID, &server.Name, &server.Type, &server.Host, &server.Port,
		&encryptedAPIKey, &server.TLSEnabled, &server.Enabled)
	if err != nil {
		return nil, err
	}
	var apiKey string
	if encryptedAPIKey.Valid && r.keyRing != nil {
		apiKey, err = r.keyRing.DecryptString(encryptedAPIKey.String)
		if err != nil {
			return nil, err
		}
	}
	factory, ok := factories[server.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported server type: %s", server.Type)
	}
	return factory(&server, apiKey), nil
}

// Invalidate must follow every successful server update or deletion. Removing a loading
// entry also prevents it from publishing a stale client when its query ends.
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
		r.disconnect(item.adapter)
	default:
		// The loader owns cleanup until it has published its result.
	}
}

func (r *Registry) disconnect(a adapter.XMPPAdapter) {
	if a != nil {
		if err := a.Disconnect(); err != nil {
			r.logger.Warn("failed to close adapter", zap.Error(err))
		}
	}
}
