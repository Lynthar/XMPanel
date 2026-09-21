// Package prosodytest is a fake Prosody 13 for tests: it speaks
// mod_http_admin_api's /admin_api/server/info and /admin_api/users and this
// repository's mod_admin_panel routes, with the status codes and bodies the
// Lua module produces.
package prosodytest

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
)

const (
	Domain = "example.com"
	Token  = "secret-token:test"
)

// Fake speaks mod_http_admin_api's /admin_api/server/info and
// /admin_api/users, and this repository's mod_admin_panel routes, with the
// status codes and bodies the Lua module produces.
type Fake struct {
	mu         sync.Mutex
	fail       int
	AdminPanel bool            // serve /admin_panel/*; false simulates the module not being installed
	users      map[string]bool // localpart -> enabled
	sessions   map[string]adapter.Session
	hosts      []string
}

func NewFake() *Fake {
	return &Fake{AdminPanel: true}
}

func (f *Fake) Reset() adaptertest.Population {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = map[string]bool{"alice": true, "bob": true, "carol": false}
	f.sessions = map[string]adapter.Session{}
	pop := adaptertest.Population{Accounts: []string{"alice@example.com", "bob@example.com", "carol@example.com"}}
	for _, s := range []adapter.Session{
		{ID: "alice@example.com/phone", AccountID: "alice@example.com"},
		{ID: "alice@example.com/desktop/2", AccountID: "alice@example.com"},
		{ID: "bob@example.com/web", AccountID: "bob@example.com"},
	} {
		f.sessions[s.ID] = s
		pop.Sessions = append(pop.Sessions, s)
	}
	return pop
}

func (f *Fake) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = status
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = append(f.hosts, r.Host)
	if f.fail != 0 {
		w.WriteHeader(f.fail)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	writeJSON := func(status int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)

	switch {
	case r.Method == http.MethodGet && path == "/admin_api/server/info":
		writeJSON(http.StatusOK, map[string]string{"site_name": Domain, "version": "13.0.5"})
	case r.Method == http.MethodGet && path == "/admin_api/users":
		var out []map[string]interface{}
		for u, enabled := range f.users {
			out = append(out, map[string]interface{}{"username": u, "enabled": enabled})
		}
		writeJSON(http.StatusOK, out)
	case !f.AdminPanel && strings.HasPrefix(path, "/admin_panel/"):
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && path == "/admin_panel/users":
		out := []map[string]interface{}{}
		for u, enabled := range f.users {
			out = append(out, map[string]interface{}{"username": u, "jid": u + "@" + Domain, "enabled": enabled})
		}
		writeJSON(http.StatusOK, out)
	case strings.HasPrefix(path, "/admin_panel/users/"):
		u := strings.TrimPrefix(path, "/admin_panel/users/")
		_, exists := f.users[u]
		switch r.Method {
		case http.MethodPut:
			if pw, _ := body["password"].(string); pw == "" {
				w.WriteHeader(http.StatusBadRequest)
			} else if exists {
				w.WriteHeader(http.StatusConflict)
			} else {
				f.users[u] = true
				writeJSON(http.StatusCreated, map[string]string{"username": u, "jid": u + "@" + Domain})
			}
		case http.MethodDelete:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
			} else {
				delete(f.users, u)
				w.WriteHeader(http.StatusNoContent)
			}
		case http.MethodPatch:
			pw, _ := body["password"].(string)
			enabled, hasEnabled := body["enabled"].(bool)
			if !exists {
				w.WriteHeader(http.StatusNotFound)
			} else if pw == "" && !hasEnabled {
				w.WriteHeader(http.StatusBadRequest)
			} else {
				if hasEnabled {
					f.users[u] = enabled
				}
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case r.Method == http.MethodGet && path == "/admin_panel/sessions":
		out := []map[string]interface{}{}
		for _, s := range f.sessions {
			_, resource, _ := strings.Cut(s.ID, "/")
			out = append(out, map[string]interface{}{
				"jid": s.ID, "bare_jid": s.AccountID, "resource": resource, "ip_address": "192.0.2.1",
				"secure": true, "priority": 1, "status": "online", "connected_at": "2026-09-20T12:00:00Z",
			})
		}
		writeJSON(http.StatusOK, out)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/admin_panel/sessions/"):
		jid := strings.TrimPrefix(path, "/admin_panel/sessions/")
		if _, ok := f.sessions[jid]; !ok {
			w.WriteHeader(http.StatusNotFound)
		} else {
			delete(f.sessions, jid)
			w.WriteHeader(http.StatusNoContent)
		}
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/admin_panel/sessions/disconnect/"):
		u := strings.TrimPrefix(path, "/admin_panel/sessions/disconnect/")
		closed := 0
		for id, s := range f.sessions {
			if s.AccountID == u+"@"+Domain {
				delete(f.sessions, id)
				closed++
			}
		}
		if closed == 0 {
			w.WriteHeader(http.StatusNotFound)
		} else {
			writeJSON(http.StatusOK, map[string]int{"closed": closed})
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Hosts returns every Host header value received so far.
func (f *Fake) Hosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hosts...)
}

// Users returns the current localpart -> enabled map.
func (f *Fake) Users() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.users))
	for k, v := range f.users {
		out[k] = v
	}
	return out
}
