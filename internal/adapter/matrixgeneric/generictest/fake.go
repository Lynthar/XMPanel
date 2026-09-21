// Package generictest is a fake spec-only homeserver for the contract tests:
// whoami, capabilities, the v1.18 lock and suspend routes, profile, whois and
// the vendor version route, answering as Continuwuity 26.9 does.
package generictest

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
)

const (
	AdminLocalpart = "admin"
	Version        = "26.9.0"
	Vendor         = "continuwuity"
)

type user struct {
	displayName string
	admin       bool
	deactivated bool
	locked      bool
	suspended   bool
}

type connection struct {
	device, ip, userAgent string
	lastSeen              int64 // milliseconds
}

// Fake is safe for concurrent use; Requests records every request that got
// past the failure injection, as "METHOD path".
type Fake struct {
	mu     sync.Mutex
	domain string
	token  string
	fail   int
	// Knobs: what the server advertises and serves.
	lock, suspend bool
	whois         bool
	federation    bool
	users         map[string]*user
	connections   map[string][]connection
	mux           *http.ServeMux
	Requests      []string
}

func New(domain, token string) *Fake {
	f := &Fake{domain: domain, token: token, lock: true, suspend: true, whois: true}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /_matrix/client/versions", f.versions)
	f.mux.HandleFunc("GET /_matrix/client/v3/account/whoami", f.whoami)
	f.mux.HandleFunc("GET /_matrix/client/v3/capabilities", f.capabilities)
	f.mux.HandleFunc("GET /_matrix/federation/v1/version", f.federationVersion)
	f.mux.HandleFunc("GET /_continuwuity/server_version", f.vendorVersion)
	f.mux.HandleFunc("GET /_continuwuity/local_user_count", f.userCount)
	f.mux.HandleFunc("GET /_matrix/client/v3/profile/{id}", f.profile)
	f.mux.HandleFunc("GET /_matrix/client/v1/admin/lock/{id}", f.getFlag)
	f.mux.HandleFunc("PUT /_matrix/client/v1/admin/lock/{id}", f.putFlag)
	f.mux.HandleFunc("GET /_matrix/client/v1/admin/suspend/{id}", f.getFlag)
	f.mux.HandleFunc("PUT /_matrix/client/v1/admin/suspend/{id}", f.putFlag)
	f.mux.HandleFunc("GET /_matrix/client/v3/admin/whois/{id}", f.whoisRoute)
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
	})
	f.Reset()
	return f
}

// Reset repopulates the fake: alice with two connections under the empty
// device key (as Synapse and Tuwunel report) and bob with one under a device
// id. The admin stays out of the population, since locking one is refused.
func (f *Fake) Reset() adaptertest.Population {
	f.mu.Lock()
	defer f.mu.Unlock()
	alice, bob := f.mxid("alice"), f.mxid("bob")
	f.users = map[string]*user{
		f.mxid(AdminLocalpart): {displayName: "Admin", admin: true},
		alice:                  {displayName: "Alice"},
		bob:                    {displayName: "Bob"},
	}
	f.connections = map[string][]connection{
		alice: {
			{ip: "10.0.0.1", userAgent: "Element/1.11", lastSeen: 1732919539393},
			{ip: "10.0.0.2", userAgent: "Element Android", lastSeen: 1732919540000},
		},
		bob: {{device: "PHONE", ip: "10.0.0.3", userAgent: "FluffyChat"}},
	}
	f.Requests = nil
	return adaptertest.Population{
		Accounts: []string{alice, bob},
		Sessions: []adapter.Session{
			{ID: "connection-1", AccountID: alice},
			{ID: "PHONE/connection-1", AccountID: bob},
			{ID: "connection-2", AccountID: alice},
		},
	}
}

func (f *Fake) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = status
}

// Moderation sets what m.account_moderation advertises to admins.
func (f *Fake) Moderation(lock, suspend bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lock, f.suspend = lock, suspend
}

// Whois switches the whois route; off, it answers M_UNRECOGNIZED like a
// server that never implemented it.
func (f *Fake) Whois(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whois = on
}

// Federation switches the federation version and user-count routes, which
// Continuwuity only serves with federation enabled.
func (f *Fake) Federation(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.federation = on
}

// SetAdmin flips whether the token's user is a server admin.
func (f *Fake) SetAdmin(admin bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[f.mxid(AdminLocalpart)].admin = admin
}

// Locked reports the lock flag of an account.
func (f *Fake) Locked(mxid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[mxid]
	return ok && u.locked
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != 0 {
		// A failure injected this way has no Matrix body, like a proxy's.
		http.Error(w, http.StatusText(f.fail), f.fail)
		return
	}
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	anonymous := r.URL.Path == "/_matrix/client/versions" || strings.HasPrefix(r.URL.Path, "/_matrix/federation/") || strings.HasPrefix(r.URL.Path, "/_continuwuity/")
	if !anonymous && r.Header.Get("Authorization") != "Bearer "+f.token {
		matrixError(w, http.StatusUnauthorized, "M_UNKNOWN_TOKEN", "Unknown access token.")
		return
	}
	f.mux.ServeHTTP(w, r)
}

func (f *Fake) mxid(localpart string) string {
	return "@" + localpart + ":" + f.domain
}

func (f *Fake) admin() bool {
	return f.users[f.mxid(AdminLocalpart)].admin
}

func (f *Fake) versions(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"versions": []string{"v1.16", "v1.17", "v1.18"}, "unstable_features": map[string]bool{}})
}

func (f *Fake) whoami(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"user_id": f.mxid(AdminLocalpart), "device_id": "PANEL", "is_guest": false})
}

// capabilities advertises m.account_moderation to admins only, as
// Continuwuity does; it is omitted when both switches would be false.
func (f *Fake) capabilities(w http.ResponseWriter, _ *http.Request) {
	caps := map[string]any{"m.room_versions": map[string]any{"default": "11", "available": map[string]string{"11": "stable"}}}
	if f.admin() && (f.lock || f.suspend) {
		caps["m.account_moderation"] = map[string]bool{"lock": f.lock, "suspend": f.suspend}
	}
	reply(w, http.StatusOK, map[string]any{"capabilities": caps})
}

func (f *Fake) federationVersion(w http.ResponseWriter, _ *http.Request) {
	if !f.federation {
		matrixError(w, http.StatusForbidden, "M_FORBIDDEN", "Federation is disabled.")
		return
	}
	reply(w, http.StatusOK, map[string]any{"server": map[string]string{"name": Vendor, "version": Version}})
}

func (f *Fake) vendorVersion(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]string{"name": Vendor, "version": Version})
}

func (f *Fake) userCount(w http.ResponseWriter, _ *http.Request) {
	if !f.federation {
		matrixError(w, http.StatusForbidden, "M_FORBIDDEN", "Federation is disabled.")
		return
	}
	n := 0
	for _, u := range f.users {
		if !u.deactivated {
			n++
		}
	}
	reply(w, http.StatusOK, map[string]int{"count": n})
}

func (f *Fake) profile(w http.ResponseWriter, r *http.Request) {
	u, ok := f.users[r.PathValue("id")]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "This user's profile could not be fetched.")
		return
	}
	reply(w, http.StatusOK, map[string]any{"displayname": u.displayName})
}

// target applies the spec's checks in the spec's order: admin first (no
// enumeration), then locality, existence and the other-admin rule.
func (f *Fake) target(w http.ResponseWriter, r *http.Request, write bool) (*user, bool) {
	if !f.admin() {
		matrixError(w, http.StatusForbidden, "M_FORBIDDEN", "Only server administrators can use this endpoint")
		return nil, false
	}
	id := r.PathValue("id")
	if _, domain := adapter.SplitMXID(id); domain != f.domain {
		matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "User does not belong to the local server")
		return nil, false
	}
	u, ok := f.users[id]
	if !ok || u.deactivated {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "This account does not exist")
		return nil, false
	}
	if u.admin && (write || id != f.mxid(AdminLocalpart)) {
		matrixError(w, http.StatusForbidden, "M_FORBIDDEN", "You cannot moderate another server administrator")
		return nil, false
	}
	return u, true
}

func (f *Fake) served(w http.ResponseWriter, r *http.Request) (field string, ok bool) {
	switch {
	case strings.Contains(r.URL.Path, "/admin/lock/") && f.lock:
		return "locked", true
	case strings.Contains(r.URL.Path, "/admin/suspend/") && f.suspend:
		return "suspended", true
	}
	matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
	return "", false
}

func (f *Fake) getFlag(w http.ResponseWriter, r *http.Request) {
	field, ok := f.served(w, r)
	if !ok {
		return
	}
	u, ok := f.target(w, r, false)
	if !ok {
		return
	}
	value := u.locked
	if field == "suspended" {
		value = u.suspended
	}
	reply(w, http.StatusOK, map[string]bool{field: value})
}

func (f *Fake) putFlag(w http.ResponseWriter, r *http.Request) {
	field, ok := f.served(w, r)
	if !ok {
		return
	}
	u, ok := f.target(w, r, true)
	if !ok {
		return
	}
	var body map[string]*bool
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body[field] == nil {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "'"+field+"' is required")
		return
	}
	if field == "suspended" {
		u.suspended = *body[field]
	} else {
		u.locked = *body[field]
	}
	reply(w, http.StatusOK, map[string]bool{field: *body[field]})
}

// whoisRoute answers the spec shape: devices keyed by device id, with the
// empty key used for connections a server does not attribute to a device.
// An unknown account gets an empty answer, as Synapse and Tuwunel give.
func (f *Fake) whoisRoute(w http.ResponseWriter, r *http.Request) {
	if !f.whois {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
		return
	}
	if !f.admin() {
		matrixError(w, http.StatusForbidden, "M_FORBIDDEN", "You are not a server admin")
		return
	}
	id := r.PathValue("id")
	byDevice := map[string][]map[string]any{}
	for _, c := range f.connections[id] {
		byDevice[c.device] = append(byDevice[c.device], map[string]any{"ip": c.ip, "last_seen": c.lastSeen, "user_agent": c.userAgent})
	}
	keys := make([]string, 0, len(byDevice))
	for key := range byDevice {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	devices := map[string]any{}
	for _, key := range keys {
		devices[key] = map[string]any{"sessions": []map[string]any{{"connections": byDevice[key]}}}
	}
	reply(w, http.StatusOK, map[string]any{"user_id": id, "devices": devices})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func matrixError(w http.ResponseWriter, status int, errcode, message string) {
	reply(w, status, map[string]string{"errcode": errcode, "error": message})
}
