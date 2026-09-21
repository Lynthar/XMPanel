// Package synapsetest is a fake Synapse for the contract and handler tests,
// answering the way 1.161 does where that matters: create-or-modify PUT, v2
// room delete accepting unknown rooms, listing quirks (locked, suspended, ts).
package synapsetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
)

const (
	AdminLocalpart = "admin"
	ServerVersion  = "1.161.0"
	// The MAS admin client the fake accepts, and the token it issues to it.
	MASClientID     = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	MASClientSecret = "mas-client-secret"
	MASToken        = "mat_fake_admin_token"
)

type user struct {
	password    string
	displayName string
	admin       bool
	deactivated bool
	locked      bool
	creationTS  int64 // seconds
	ulid        string
	masAdmin    bool // MAS's can_request_admin; Synapse's admin column is separate
}

type device struct {
	id, displayName, ip, userAgent string
	lastSeen                       int64 // milliseconds
}

type room struct {
	name, alias, version, creator string
	joined, local                 int
	public                        bool
}

// Fake is safe for concurrent use; Requests records every request that got
// past the failure injection, as "METHOD path?query".
type Fake struct {
	mu     sync.Mutex
	domain string
	token  string
	fail   int
	mas    bool
	// MAS knobs: password login off makes set-password answer 403; a set-
	// password failure injection answers 500; a slow token delays the grant.
	noPasswords     bool
	failSetPassword bool
	slowToken       time.Duration
	users           map[string]*user
	devices         map[string][]device
	rooms           map[string]*room
	deletes         int
	sequence        int
	mux             *http.ServeMux
	Requests        []string
}

var localpartPattern = regexp.MustCompile(`^[a-z0-9._=/+-]+$`)

func New(domain, token string) *Fake {
	f := &Fake{domain: domain, token: token}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /_matrix/client/versions", f.versions)
	f.mux.HandleFunc("GET /_matrix/client/v3/account/whoami", f.whoami)
	f.mux.HandleFunc("GET /_matrix/client/v1/auth_metadata", f.authMetadata)
	f.mux.HandleFunc("GET /_synapse/admin/v1/server_version", f.serverVersion)
	f.mux.HandleFunc("GET /_synapse/admin/v3/users", f.listUsers)
	f.mux.HandleFunc("GET /_synapse/admin/v2/users/{id}", f.getUser)
	f.mux.HandleFunc("PUT /_synapse/admin/v2/users/{id}", f.putUser)
	f.mux.HandleFunc("POST /_synapse/admin/v1/deactivate/{id}", f.deactivate)
	f.mux.HandleFunc("PUT /_synapse/admin/v1/users/{id}/admin", f.setAdmin)
	f.mux.HandleFunc("GET /_synapse/admin/v2/users/{id}/devices", f.listDevices)
	f.mux.HandleFunc("DELETE /_synapse/admin/v2/users/{id}/devices/{device}", f.deleteDevice)
	f.mux.HandleFunc("POST /_synapse/admin/v2/users/{id}/delete_devices", f.deleteDevices)
	f.mux.HandleFunc("GET /_synapse/admin/v1/rooms", f.listRooms)
	f.mux.HandleFunc("GET /_synapse/admin/v1/rooms/{id}", f.getRoom)
	f.mux.HandleFunc("DELETE /_synapse/admin/v2/rooms/{id}", f.deleteRoom)
	f.mux.HandleFunc("POST /oauth2/token", f.masToken)
	f.mux.HandleFunc("GET /api/admin/v1/site-config", f.masSiteConfig)
	f.mux.HandleFunc("GET /api/admin/v1/users/by-username/{username}", f.masUserByUsername)
	f.mux.HandleFunc("GET /api/admin/v1/users", f.masListUsers)
	f.mux.HandleFunc("POST /api/admin/v1/users", f.masCreateUser)
	f.mux.HandleFunc("POST /api/admin/v1/users/{id}/{action}", f.masUserAction)
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
	})
	f.Reset()
	return f
}

// Reset repopulates the fake: the admin plus two accounts, three devices
// and two rooms.
func (f *Fake) Reset() adaptertest.Population {
	f.mu.Lock()
	defer f.mu.Unlock()
	admin, alice, bob := f.mxid(AdminLocalpart), f.mxid("alice"), f.mxid("bob")
	f.users = map[string]*user{
		admin: {displayName: "Admin", admin: true, masAdmin: true, creationTS: 1560432506},
		alice: {displayName: "Alice", creationTS: 1561550621},
		bob:   {displayName: "Bob", creationTS: 1562000000},
	}
	for _, u := range f.users {
		u.ulid = f.nextULID()
	}
	f.devices = map[string][]device{
		alice: {{id: "DEV1", displayName: "laptop", ip: "10.0.0.1", userAgent: "Element/1.11", lastSeen: 1732919539393}},
		bob: {
			{id: "DEV2", displayName: "phone", ip: "10.0.0.2", userAgent: "Element Android", lastSeen: 1732919540000},
			{id: "DEV3", displayName: "tablet"},
		},
	}
	f.rooms = map[string]*room{
		"!room1:" + f.domain: {name: "Room One", alias: "#one:" + f.domain, version: "10", creator: alice, joined: 2, local: 2, public: true},
		"!room2:" + f.domain: {version: "9", creator: bob, joined: 1, local: 1},
	}
	f.Requests = nil
	return adaptertest.Population{
		Accounts: []string{admin, alice, bob},
		Sessions: []adapter.Session{
			{ID: "DEV1", AccountID: alice},
			{ID: "DEV2", AccountID: bob},
			{ID: "DEV3", AccountID: bob},
		},
		Rooms: []string{"!room1:" + f.domain, "!room2:" + f.domain},
	}
}

func (f *Fake) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = status
}

// DelegateAuth switches the fake to a deployment whose authentication is
// delegated to MAS: auth_metadata answers 200, the admin-bit endpoint is gone
// as in Synapse, and MAS's token endpoint and admin API appear on this fake.
func (f *Fake) DelegateAuth(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mas = on
}

// PasswordLogin switches MAS's password login; off, set-password answers 403.
func (f *Fake) PasswordLogin(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noPasswords = !enabled
}

// FailSetPassword makes MAS's set-password answer 500 until switched off.
func (f *Fake) FailSetPassword(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSetPassword = on
}

// SlowToken delays every token grant by d.
func (f *Fake) SlowToken(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slowToken = d
}

// SetHomeserverAdmin flips Synapse's own admin column, which MAS never writes.
func (f *Fake) SetHomeserverAdmin(mxid string, admin bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[mxid]; ok {
		u.admin = admin
	}
}

// Deactivated reports whether an account exists and is deactivated.
func (f *Fake) Deactivated(mxid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[mxid]
	return ok && u.deactivated
}

// HasUser reports whether an account exists, deactivated or not.
func (f *Fake) HasUser(mxid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[mxid]
	return ok
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != 0 {
		// A failure injected this way has no Matrix body, like a proxy's.
		http.Error(w, http.StatusText(f.fail), f.fail)
		return
	}
	f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
	switch {
	case r.URL.Path == "/_matrix/client/versions" || r.URL.Path == "/_matrix/client/v1/auth_metadata" || r.URL.Path == "/oauth2/token":
	case strings.HasPrefix(r.URL.Path, "/api/admin/"):
		if !f.mas {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+MASToken {
			masError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
	case r.Header.Get("Authorization") != "Bearer "+f.token:
		matrixError(w, http.StatusUnauthorized, "M_UNKNOWN_TOKEN", "Invalid access token passed.")
		return
	}
	f.mux.ServeHTTP(w, r)
}

func (f *Fake) mxid(localpart string) string {
	return "@" + localpart + ":" + f.domain
}

func (f *Fake) local(mxid string) bool {
	_, domain := adapter.SplitMXID(mxid)
	return domain == f.domain
}

func (f *Fake) versions(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"versions": []string{"v1.11", "v1.12", "v1.13"}, "unstable_features": map[string]bool{}})
}

func (f *Fake) whoami(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"user_id": f.mxid(AdminLocalpart), "device_id": "PANEL", "is_guest": false})
}

func (f *Fake) authMetadata(w http.ResponseWriter, _ *http.Request) {
	if !f.mas {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
		return
	}
	issuer := "https://auth." + f.domain + "/"
	reply(w, http.StatusOK, map[string]any{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + "authorize",
		"token_endpoint":         issuer + "oauth2/token",
	})
}

func (f *Fake) serverVersion(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]string{"server_version": ServerVersion})
}

func (f *Fake) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, limit, ok := paging(w, q)
	if !ok {
		return
	}
	if f.mas && q.Get("guests") == "true" {
		matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "The guests parameter is not supported when delegating to MAS.")
		return
	}
	needle := strings.ToLower(q.Get("name"))
	deactivated := q.Get("deactivated")
	includeLocked := q.Get("locked") == "true"
	ids := make([]string, 0, len(f.users))
	for id, u := range f.users {
		if deactivated == "true" && !u.deactivated || deactivated == "false" && u.deactivated {
			continue
		}
		if u.locked && !includeLocked {
			continue
		}
		localpart, _ := adapter.SplitMXID(id)
		if needle != "" && !strings.Contains(localpart, needle) && !strings.Contains(strings.ToLower(u.displayName), needle) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	total := len(ids)
	end := from + limit
	if end > total {
		end = total
	}
	if from > total {
		from = total
	}
	users := make([]map[string]any, 0, end-from)
	for _, id := range ids[from:end] {
		u := f.users[id]
		entry := f.userJSON(id, u)
		entry["creation_ts"] = u.creationTS * 1000
		users = append(users, entry)
	}
	out := map[string]any{"users": users, "total": total}
	if end < total {
		out["next_token"] = strconv.Itoa(end)
	}
	reply(w, http.StatusOK, out)
}

func (f *Fake) userJSON(id string, u *user) map[string]any {
	return map[string]any{
		"name":          id,
		"displayname":   u.displayName,
		"admin":         u.admin,
		"deactivated":   u.deactivated,
		"locked":        u.locked,
		"shadow_banned": false,
		"erased":        false,
		"is_guest":      false,
		"user_type":     nil,
		"avatar_url":    nil,
		"creation_ts":   u.creationTS,
	}
}

func (f *Fake) getUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, ok := f.users[id]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
		return
	}
	entry := f.userJSON(id, u)
	entry["suspended"] = false
	entry["last_seen_ts"] = nil
	entry["threepids"] = []any{}
	entry["external_ids"] = []any{}
	reply(w, http.StatusOK, entry)
}

func (f *Fake) putUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "This endpoint can only be used with local users")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		matrixError(w, http.StatusBadRequest, "M_NOT_JSON", "Content not JSON.")
		return
	}
	u, exists := f.users[id]
	status := http.StatusOK
	if !exists {
		if localpart, _ := adapter.SplitMXID(id); !localpartPattern.MatchString(localpart) {
			matrixError(w, http.StatusBadRequest, "M_INVALID_USERNAME", "User ID can only contain characters a-z, 0-9, or '=_-./+'")
			return
		}
		u = &user{creationTS: 1700000000}
		f.users[id] = u
		status = http.StatusCreated
	}
	locked, hasLocked := body["locked"].(bool)
	deactivated, hasDeactivated := body["deactivated"].(bool)
	if hasLocked && locked && (deactivated || u.deactivated) || hasDeactivated && deactivated && (locked || u.locked) {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "An user can't be deactivated and locked")
		return
	}
	if password, ok := body["password"].(string); ok {
		u.password = password
		if logout, ok := body["logout_devices"].(bool); exists && (!ok || logout) {
			delete(f.devices, id)
		}
	}
	if name, ok := body["displayname"].(string); ok {
		u.displayName = name
	}
	if admin, ok := body["admin"].(bool); ok {
		u.admin = admin
	}
	if hasLocked {
		u.locked = locked
	}
	if hasDeactivated {
		u.deactivated = deactivated
		if deactivated {
			delete(f.devices, id)
		}
	}
	reply(w, status, f.userJSON(id, u))
}

func (f *Fake) deactivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Can only deactivate local users")
		return
	}
	u, ok := f.users[id]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
		return
	}
	u.deactivated = true
	u.locked = false
	delete(f.devices, id)
	reply(w, http.StatusOK, map[string]string{"id_server_unbind_result": "success"})
}

func (f *Fake) setAdmin(w http.ResponseWriter, r *http.Request) {
	if f.mas {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
		return
	}
	id := r.PathValue("id")
	u, ok := f.users[id]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
		return
	}
	var body struct {
		Admin *bool `json:"admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Admin == nil {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "'admin' parameter is required")
		return
	}
	u.admin = *body.Admin
	reply(w, http.StatusOK, map[string]any{})
}

func (f *Fake) requireUser(w http.ResponseWriter, id string) bool {
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Can only lookup local users")
		return false
	}
	if _, ok := f.users[id]; !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "Unknown user")
		return false
	}
	return true
}

func (f *Fake) listDevices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.requireUser(w, id) {
		return
	}
	devices := make([]map[string]any, 0)
	for _, d := range f.devices[id] {
		entry := map[string]any{
			"device_id":            d.id,
			"display_name":         nilIfEmpty(d.displayName),
			"last_seen_ip":         nilIfEmpty(d.ip),
			"last_seen_user_agent": nilIfEmpty(d.userAgent),
			"last_seen_ts":         nil,
			"user_id":              id,
		}
		if d.lastSeen > 0 {
			entry["last_seen_ts"] = d.lastSeen
		}
		devices = append(devices, entry)
	}
	reply(w, http.StatusOK, map[string]any{"devices": devices, "total": len(devices)})
}

func (f *Fake) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.requireUser(w, id) {
		return
	}
	f.removeDevices(id, []string{r.PathValue("device")})
	reply(w, http.StatusOK, map[string]any{})
}

func (f *Fake) deleteDevices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.requireUser(w, id) {
		return
	}
	var body struct {
		Devices []string `json:"devices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		matrixError(w, http.StatusBadRequest, "M_NOT_JSON", "Content not JSON.")
		return
	}
	f.removeDevices(id, body.Devices)
	reply(w, http.StatusOK, map[string]any{})
}

// removeDevices ignores unknown device ids, as Synapse does.
func (f *Fake) removeDevices(id string, ids []string) {
	gone := map[string]bool{}
	for _, d := range ids {
		gone[d] = true
	}
	kept := f.devices[id][:0]
	for _, d := range f.devices[id] {
		if !gone[d.id] {
			kept = append(kept, d)
		}
	}
	f.devices[id] = kept
}

func (f *Fake) listRooms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, limit, ok := paging(w, q)
	if !ok {
		return
	}
	if _, present := q["search_term"]; present && q.Get("search_term") == "" {
		matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "search_term cannot be an empty string")
		return
	}
	needle := q.Get("search_term")
	ids := make([]string, 0, len(f.rooms))
	for id, rm := range f.rooms {
		aliasLocal, _, _ := strings.Cut(strings.TrimPrefix(rm.alias, "#"), ":")
		if needle != "" && !strings.Contains(rm.name, needle) && !strings.Contains(aliasLocal, needle) && id != needle {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if f.rooms[ids[i]].name != f.rooms[ids[j]].name {
			return f.rooms[ids[i]].name < f.rooms[ids[j]].name
		}
		return ids[i] < ids[j]
	})
	total := len(ids)
	end := from + limit
	if end > total {
		end = total
	}
	if from > total {
		from = total
	}
	rooms := make([]map[string]any, 0, end-from)
	for _, id := range ids[from:end] {
		rooms = append(rooms, f.roomJSON(id, f.rooms[id]))
	}
	out := map[string]any{"rooms": rooms, "offset": from, "total_rooms": total}
	if end < total {
		out["next_batch"] = end
	}
	if from > 0 {
		out["prev_batch"] = max(from-limit, 0)
	}
	reply(w, http.StatusOK, out)
}

func (f *Fake) roomJSON(id string, rm *room) map[string]any {
	joinRules := "invite"
	if rm.public {
		joinRules = "public"
	}
	return map[string]any{
		"room_id":              id,
		"name":                 nilIfEmpty(rm.name),
		"canonical_alias":      nilIfEmpty(rm.alias),
		"joined_members":       rm.joined,
		"joined_local_members": rm.local,
		"version":              rm.version,
		"creator":              rm.creator,
		"encryption":           nil,
		"federatable":          true,
		"public":               rm.public,
		"join_rules":           joinRules,
		"guest_access":         nil,
		"history_visibility":   "shared",
		"state_events":         12,
		"room_type":            nil,
	}
}

func (f *Fake) getRoom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rm, ok := f.rooms[id]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "Room not found")
		return
	}
	entry := f.roomJSON(id, rm)
	entry["topic"] = nil
	entry["avatar"] = nil
	entry["joined_local_devices"] = rm.local
	entry["forgotten"] = false
	entry["tombstoned"] = false
	entry["replacement_room"] = nil
	reply(w, http.StatusOK, entry)
}

// deleteRoom answers a delete_id for any well-formed id, as Synapse does:
// the purge runs in the background and only its status reports a missing
// room. The fake completes it at once.
func (f *Fake) deleteRoom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "!") || !strings.Contains(id, ":") {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", id+" is not a legal room ID")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		matrixError(w, http.StatusBadRequest, "M_NOT_JSON", "Content not JSON.")
		return
	}
	for _, key := range []string{"block", "purge", "force_purge"} {
		if v, present := body[key]; present {
			if _, isBool := v.(bool); !isBool {
				matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "Param '"+key+"' must be a boolean, if given")
				return
			}
		}
	}
	delete(f.rooms, id)
	f.deletes++
	reply(w, http.StatusOK, map[string]string{"delete_id": "delete-" + strconv.Itoa(f.deletes)})
}

func paging(w http.ResponseWriter, q map[string][]string) (from, limit int, ok bool) {
	from, limit = 0, 100
	var err error
	if v := first(q, "from"); v != "" {
		if from, err = strconv.Atoi(v); err != nil || from < 0 {
			matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "Query parameter from must be a non-negative integer.")
			return 0, 0, false
		}
	}
	if v := first(q, "limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "Query parameter limit must be a non-negative integer.")
			return 0, 0, false
		}
	}
	return from, limit, true
}

func first(q map[string][]string, key string) string {
	if v := q[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func matrixError(w http.ResponseWriter, status int, errcode, message string) {
	reply(w, status, map[string]string{"errcode": errcode, "error": message})
}

// masToken answers the client_credentials grant for the configured client.
func (f *Fake) masToken(w http.ResponseWriter, r *http.Request) {
	if !f.mas {
		http.NotFound(w, r)
		return
	}
	id, secret, ok := r.BasicAuth()
	if err := r.ParseForm(); err != nil || !ok || id != MASClientID || secret != MASClientSecret {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "Client authentication failed"})
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != "urn:mas:admin" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "Unsupported grant or scope"})
		return
	}
	if f.slowToken > 0 {
		time.Sleep(f.slowToken)
	}
	reply(w, http.StatusOK, map[string]any{"access_token": MASToken, "token_type": "Bearer", "expires_in": 300, "scope": "urn:mas:admin"})
}

func (f *Fake) masSiteConfig(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{
		"server_name": f.domain, "password_login_enabled": !f.noPasswords, "password_registration_enabled": false,
		"minimum_password_complexity": 3,
	})
}

// masListUsers serves the admin filter only, in one page, which is what the
// adapter's admin overlay asks for.
func (f *Fake) masListUsers(w http.ResponseWriter, r *http.Request) {
	adminsOnly := r.URL.Query().Get("filter[admin]") == "true"
	ids := make([]string, 0, len(f.users))
	for id, u := range f.users {
		if !adminsOnly || u.masAdmin {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, f.masResource(id, f.users[id])["data"])
	}
	reply(w, http.StatusOK, map[string]any{
		"meta": map[string]int{"count": len(data)}, "data": data,
		"links": map[string]any{"self": r.URL.RequestURI(), "next": nil},
	})
}

// masResource renders a user the way the admin API does: a ULID id and the
// lock and deactivation as timestamps.
func (f *Fake) masResource(id string, u *user) map[string]any {
	localpart, _ := adapter.SplitMXID(id)
	attributes := map[string]any{
		"username": localpart, "created_at": time.Unix(u.creationTS, 0).UTC().Format(time.RFC3339),
		"locked_at": nil, "deactivated_at": nil, "admin": u.masAdmin, "legacy_guest": false,
	}
	if u.locked {
		attributes["locked_at"] = "2026-01-01T00:00:00Z"
	}
	if u.deactivated {
		attributes["deactivated_at"] = "2026-01-01T00:00:00Z"
	}
	self := "/api/admin/v1/users/" + u.ulid
	return map[string]any{
		"data":  map[string]any{"type": "user", "id": u.ulid, "attributes": attributes, "links": map[string]string{"self": self}},
		"links": map[string]string{"self": self},
	}
}

func (f *Fake) masUserByUsername(w http.ResponseWriter, r *http.Request) {
	id := f.mxid(r.PathValue("username"))
	u, ok := f.users[id]
	if !ok {
		masError(w, http.StatusNotFound, "User not found")
		return
	}
	reply(w, http.StatusOK, f.masResource(id, u))
}

// masCreateUser registers the account; the fake provisions it into the
// Synapse side at once, where the real MAS does so in the background.
func (f *Fake) masCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string  `json:"username"`
		DisplayName *string `json:"displayname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" {
		masError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if !localpartPattern.MatchString(body.Username) {
		masError(w, http.StatusBadRequest, "Invalid username")
		return
	}
	id := f.mxid(body.Username)
	if _, exists := f.users[id]; exists {
		masError(w, http.StatusConflict, "User already exists")
		return
	}
	u := &user{creationTS: 1700000000, ulid: f.nextULID()}
	if body.DisplayName != nil {
		u.displayName = *body.DisplayName
	}
	f.users[id] = u
	reply(w, http.StatusCreated, f.masResource(id, u))
}

// masUserAction handles the per-user actions. The password rule stands in
// for MAS's complexity check: eight characters unless the check is skipped.
func (f *Fake) masUserAction(w http.ResponseWriter, r *http.Request) {
	id, u := f.userByULID(r.PathValue("id"))
	if u == nil {
		masError(w, http.StatusNotFound, "User not found")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch r.PathValue("action") {
	case "set-password":
		if f.noPasswords {
			masError(w, http.StatusForbidden, "Password authentication is disabled")
			return
		}
		if f.failSetPassword {
			masError(w, http.StatusInternalServerError, "Internal error")
			return
		}
		password, _ := body["password"].(string)
		skip, _ := body["skip_password_check"].(bool)
		if password == "" {
			masError(w, http.StatusBadRequest, "Invalid request")
			return
		}
		if !skip && len(password) < 8 {
			masError(w, http.StatusBadRequest, "Password is too weak")
			return
		}
		u.password = password
		w.WriteHeader(http.StatusNoContent)
		return
	case "lock":
		u.locked = true
	case "unlock":
		u.locked = false
	case "deactivate":
		u.deactivated = true
		u.locked = false
		delete(f.devices, id)
	case "reactivate":
		u.deactivated = false
	case "set-admin":
		admin, ok := body["admin"].(bool)
		if !ok {
			masError(w, http.StatusBadRequest, "Invalid request")
			return
		}
		u.masAdmin = admin
	default:
		masError(w, http.StatusNotFound, "Not found")
		return
	}
	reply(w, http.StatusOK, f.masResource(id, u))
}

func (f *Fake) userByULID(ulid string) (string, *user) {
	for id, u := range f.users {
		if u.ulid == ulid {
			return id, u
		}
	}
	return "", nil
}

// nextULID yields a distinct 26-character id; digits are valid Crockford
// base32, so it has the shape of a real ULID.
func (f *Fake) nextULID() string {
	f.sequence++
	return fmt.Sprintf("%026d", f.sequence)
}

func masError(w http.ResponseWriter, status int, title string) {
	reply(w, status, map[string]any{"errors": []map[string]string{{"title": title}}})
}
