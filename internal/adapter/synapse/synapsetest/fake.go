// Package synapsetest is a fake Synapse for the contract and handler tests,
// answering the way 1.161 does where that matters: create-or-modify PUT, v2
// room delete accepting unknown rooms, listing quirks (locked, suspended, ts).
package synapsetest

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
)

const (
	AdminLocalpart = "admin"
	ServerVersion  = "1.161.0"
)

type user struct {
	password    string
	displayName string
	admin       bool
	deactivated bool
	locked      bool
	creationTS  int64 // seconds
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
	mu       sync.Mutex
	domain   string
	token    string
	fail     int
	mas      bool
	users    map[string]*user
	devices  map[string][]device
	rooms    map[string]*room
	deletes  int
	mux      *http.ServeMux
	Requests []string
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
		admin: {displayName: "Admin", admin: true, creationTS: 1560432506},
		alice: {displayName: "Alice", creationTS: 1561550621},
		bob:   {displayName: "Bob", creationTS: 1562000000},
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
// delegated to MAS: auth_metadata answers 200 and the admin-bit endpoint is
// gone, as in Synapse.
func (f *Fake) DelegateAuth(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mas = on
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
	public := r.URL.Path == "/_matrix/client/versions" || r.URL.Path == "/_matrix/client/v1/auth_metadata"
	if !public && r.Header.Get("Authorization") != "Bearer "+f.token {
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
