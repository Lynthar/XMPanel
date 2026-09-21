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
	password     string
	displayName  string
	admin        bool
	deactivated  bool
	locked       bool
	suspended    bool
	shadowBanned bool
	erased       bool
	creationTS   int64 // seconds
	ulid         string
	masAdmin     bool // MAS's can_request_admin; Synapse's admin column is separate
}

type media struct {
	id, mediaType, name, owner string
	size                       int64
	createdTS                  int64
	quarantined                bool
}

type regToken struct {
	token       string
	usesAllowed *int
	pending     int
	completed   int
	expiryTime  *int64
	ulid        string // MAS
	revoked     bool   // MAS
	createdAt   string // MAS
}

type report struct {
	id                                           int64
	receivedTS                                   int64
	roomID, name, alias, eventID, userID, sender string
	reason                                       string
	score                                        int
}

type device struct {
	id, displayName, ip, userAgent string
	lastSeen                       int64 // milliseconds
}

type room struct {
	name, alias, version, creator string
	joined, local                 int
	public                        bool
	blocked                       bool
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
	media           map[string]*media
	tokens          map[string]*regToken
	reports         []report
	notices         map[string][]string // notices sent, by recipient
	noNotices       bool                // server_notices not configured
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
	f.mux.HandleFunc("GET /_synapse/admin/v2/users/{id}/devices", f.listDevices)
	f.mux.HandleFunc("DELETE /_synapse/admin/v2/users/{id}/devices/{device}", f.deleteDevice)
	f.mux.HandleFunc("POST /_synapse/admin/v2/users/{id}/delete_devices", f.deleteDevices)
	f.mux.HandleFunc("GET /_synapse/admin/v1/rooms", f.listRooms)
	f.mux.HandleFunc("GET /_synapse/admin/v1/rooms/{id}", f.getRoom)
	f.mux.HandleFunc("DELETE /_synapse/admin/v2/rooms/{id}", f.deleteRoom)
	f.mux.HandleFunc("PUT /_synapse/admin/v1/suspend/{id}", f.suspend)
	f.mux.HandleFunc("POST /_synapse/admin/v1/users/{id}/shadow_ban", f.shadowBan)
	f.mux.HandleFunc("DELETE /_synapse/admin/v1/users/{id}/shadow_ban", f.shadowBan)
	f.mux.HandleFunc("PUT /_synapse/admin/v1/users/{id}/admin", f.setAdmin)
	f.mux.HandleFunc("GET /_synapse/admin/v1/registration_tokens", f.listTokens)
	f.mux.HandleFunc("POST /_synapse/admin/v1/registration_tokens/new", f.createToken)
	f.mux.HandleFunc("DELETE /_synapse/admin/v1/registration_tokens/{token}", f.deleteToken)
	f.mux.HandleFunc("GET /_synapse/admin/v1/event_reports", f.listReports)
	f.mux.HandleFunc("GET /_synapse/admin/v1/users/{id}/media", f.listMedia)
	f.mux.HandleFunc("POST /_synapse/admin/v1/user/{id}/media/quarantine", f.quarantineMedia)
	f.mux.HandleFunc("DELETE /_synapse/admin/v1/media/{server}/{media}", f.deleteMedia)
	f.mux.HandleFunc("PUT /_synapse/admin/v1/rooms/{id}/block", f.blockRoom)
	f.mux.HandleFunc("POST /_synapse/admin/v1/send_server_notice", f.serverNotice)
	f.mux.HandleFunc("GET /_synapse/admin/v1/federation/destinations", f.listDestinations)
	f.mux.HandleFunc("POST /oauth2/token", f.masToken)
	f.mux.HandleFunc("GET /api/admin/v1/site-config", f.masSiteConfig)
	f.mux.HandleFunc("GET /api/admin/v1/users/by-username/{username}", f.masUserByUsername)
	f.mux.HandleFunc("GET /api/admin/v1/users", f.masListUsers)
	f.mux.HandleFunc("POST /api/admin/v1/users", f.masCreateUser)
	f.mux.HandleFunc("POST /api/admin/v1/users/{id}/{action}", f.masUserAction)
	f.mux.HandleFunc("GET /api/admin/v1/user-registration-tokens", f.masListTokens)
	f.mux.HandleFunc("POST /api/admin/v1/user-registration-tokens", f.masCreateToken)
	f.mux.HandleFunc("POST /api/admin/v1/user-registration-tokens/{id}/{action}", f.masTokenAction)
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
	})
	f.Reset()
	return f
}

// Reset repopulates the fake: the admin plus two accounts, three devices,
// two rooms, two media items of alice's, one registration token and one
// event report.
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
	f.media = map[string]*media{
		"MEDIA1": {id: "MEDIA1", mediaType: "image/png", name: "one.png", owner: admin, size: 67, createdTS: 1732919539393},
		"MEDIA2": {id: "MEDIA2", mediaType: "application/octet-stream", owner: admin, size: 1337, createdTS: 1732919540000},
	}
	f.tokens = map[string]*regToken{"seed-token": {token: "seed-token", completed: 1, ulid: f.nextULID(), createdAt: "2026-01-01T00:00:00Z"}}
	f.reports = []report{{id: 2, receivedTS: 1570897107409, roomID: "!room1:" + f.domain, name: "Room One", alias: "#one:" + f.domain, eventID: "$event1", userID: bob, sender: alice, reason: "spam", score: -100}}
	f.notices = map[string][]string{}
	f.Requests = nil
	return adaptertest.Population{
		Accounts: []string{admin, alice, bob},
		Sessions: []adapter.Session{
			{ID: "DEV1", AccountID: alice},
			{ID: "DEV2", AccountID: bob},
			{ID: "DEV3", AccountID: bob},
		},
		Rooms: []string{"!room1:" + f.domain, "!room2:" + f.domain},
		Media: []string{"MEDIA1", "MEDIA2"},
	}
}

// ServerNotices switches the server_notices configuration; off, the notice
// endpoint answers Synapse's 400.
func (f *Fake) ServerNotices(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noNotices = !enabled
}

// Notices returns the notice bodies sent to an account.
func (f *Fake) Notices(mxid string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notices[mxid]...)
}

// Erased reports whether an account was deactivated with erase.
func (f *Fake) Erased(mxid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[mxid]
	return ok && u.erased
}

// Blocked reports whether a room id is blocked, known or not.
func (f *Fake) Blocked(roomID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rooms[roomID]
	return ok && r.blocked
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
		"shadow_banned": u.shadowBanned,
		"erased":        u.erased,
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
	entry["suspended"] = u.suspended
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
	var body struct {
		Erase bool `json:"erase"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	u.deactivated = true
	u.locked = false
	u.erased = u.erased || body.Erase
	delete(f.devices, id)
	reply(w, http.StatusOK, map[string]string{"id_server_unbind_result": "success"})
}

func (f *Fake) suspend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Can only suspend local users")
		return
	}
	u, ok := f.users[id]
	if !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
		return
	}
	var body struct {
		Suspend *bool `json:"suspend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Suspend == nil {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "'suspend' parameter is required")
		return
	}
	u.suspended = *body.Suspend
	reply(w, http.StatusOK, map[string]any{"user_id": id, "suspend": u.suspended})
}

func (f *Fake) shadowBan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Only local users can be shadow-banned")
		return
	}
	u, ok := f.users[id]
	if !ok {
		// Synapse's servlet skips the lookup; the store's update raises this.
		matrixError(w, http.StatusNotFound, "M_UNKNOWN", "No row found (users)")
		return
	}
	u.shadowBanned = r.Method == http.MethodPost
	reply(w, http.StatusOK, map[string]any{})
}

func (f *Fake) tokenJSON(t *regToken) map[string]any {
	return map[string]any{
		"token": t.token, "uses_allowed": t.usesAllowed, "pending": t.pending, "completed": t.completed, "expiry_time": t.expiryTime,
	}
}

// Synapse does not register the registration token servlets under MAS.
func (f *Fake) tokensServed(w http.ResponseWriter) bool {
	if f.mas {
		matrixError(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
		return false
	}
	return true
}

func (f *Fake) listTokens(w http.ResponseWriter, _ *http.Request) {
	if !f.tokensServed(w) {
		return
	}
	ids := make([]string, 0, len(f.tokens))
	for id, t := range f.tokens {
		if !t.revoked {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.tokenJSON(f.tokens[id]))
	}
	reply(w, http.StatusOK, map[string]any{"registration_tokens": out})
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)

func (f *Fake) createToken(w http.ResponseWriter, r *http.Request) {
	if !f.tokensServed(w) {
		return
	}
	var body struct {
		Token       *string `json:"token"`
		UsesAllowed *int    `json:"uses_allowed"`
		ExpiryTime  *int64  `json:"expiry_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		matrixError(w, http.StatusBadRequest, "M_NOT_JSON", "Content not JSON.")
		return
	}
	token := "gen-" + strconv.Itoa(len(f.tokens)+1)
	if body.Token != nil {
		if !tokenPattern.MatchString(*body.Token) {
			matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "token must consist of characters matched by the regex [A-Za-z0-9-_]")
			return
		}
		if _, exists := f.tokens[*body.Token]; exists {
			matrixError(w, http.StatusBadRequest, "M_INVALID_PARAM", "Token already exists: "+*body.Token)
			return
		}
		token = *body.Token
	}
	t := &regToken{token: token, usesAllowed: body.UsesAllowed, expiryTime: body.ExpiryTime, ulid: f.nextULID(), createdAt: "2026-01-02T00:00:00Z"}
	f.tokens[token] = t
	reply(w, http.StatusOK, f.tokenJSON(t))
}

func (f *Fake) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !f.tokensServed(w) {
		return
	}
	token := r.PathValue("token")
	if _, ok := f.tokens[token]; !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "No such registration token: "+token)
		return
	}
	delete(f.tokens, token)
	reply(w, http.StatusOK, map[string]any{})
}

func (f *Fake) listReports(w http.ResponseWriter, r *http.Request) {
	from, limit, ok := paging(w, r.URL.Query())
	if !ok {
		return
	}
	total := len(f.reports)
	end := min(from+limit, total)
	from = min(from, total)
	out := make([]map[string]any, 0)
	for _, rp := range f.reports[from:end] {
		out = append(out, map[string]any{
			"id": rp.id, "received_ts": rp.receivedTS, "room_id": rp.roomID, "name": rp.name, "canonical_alias": rp.alias,
			"event_id": rp.eventID, "user_id": rp.userID, "sender": rp.sender, "reason": rp.reason, "score": rp.score,
		})
	}
	resp := map[string]any{"event_reports": out, "total": total}
	if end < total {
		resp["next_token"] = end
	}
	reply(w, http.StatusOK, resp)
}

func (f *Fake) listMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.requireUser(w, id) {
		return
	}
	from, limit, ok := paging(w, r.URL.Query())
	if !ok {
		return
	}
	ids := make([]string, 0)
	for mid, md := range f.media {
		if md.owner == id {
			ids = append(ids, mid)
		}
	}
	sort.Strings(ids)
	total := len(ids)
	end := min(from+limit, total)
	from = min(from, total)
	out := make([]map[string]any, 0)
	for _, mid := range ids[from:end] {
		md := f.media[mid]
		entry := map[string]any{
			"media_id": md.id, "media_type": md.mediaType, "media_length": md.size, "upload_name": nilIfEmpty(md.name),
			"created_ts": md.createdTS, "last_access_ts": nil, "quarantined_by": nil, "safe_from_quarantine": false,
		}
		if md.quarantined {
			entry["quarantined_by"] = f.mxid(AdminLocalpart)
		}
		out = append(out, entry)
	}
	resp := map[string]any{"media": out, "total": total}
	if end < total {
		resp["next_token"] = end
	}
	reply(w, http.StatusOK, resp)
}

func (f *Fake) quarantineMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !f.local(id) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Can only quarantine local users' media")
		return
	}
	n := 0
	for _, md := range f.media {
		if md.owner == id && !md.quarantined {
			md.quarantined = true
			n++
		}
	}
	reply(w, http.StatusOK, map[string]int{"num_quarantined": n})
}

func (f *Fake) deleteMedia(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("server") != f.domain {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Can only delete local media")
		return
	}
	mid := r.PathValue("media")
	if _, ok := f.media[mid]; !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "Unknown media")
		return
	}
	delete(f.media, mid)
	reply(w, http.StatusOK, map[string]any{"deleted_media": []string{mid}, "total": 1})
}

// blockRoom accepts unknown rooms, as Synapse does, but only records the
// flag on ones it has.
func (f *Fake) blockRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Block *bool `json:"block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Block == nil {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "Param 'block' must be a boolean")
		return
	}
	if rm, ok := f.rooms[r.PathValue("id")]; ok {
		rm.blocked = *body.Block
	}
	reply(w, http.StatusOK, map[string]bool{"block": *body.Block})
}

func (f *Fake) serverNotice(w http.ResponseWriter, r *http.Request) {
	if f.noNotices {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Server notices are not enabled on this server")
		return
	}
	var body struct {
		UserID  string `json:"user_id"`
		Content struct {
			Body string `json:"body"`
		} `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" {
		matrixError(w, http.StatusBadRequest, "M_BAD_JSON", "'user_id' is required")
		return
	}
	if !f.local(body.UserID) {
		matrixError(w, http.StatusBadRequest, "M_UNKNOWN", "Server notices can only be sent to local users")
		return
	}
	if _, ok := f.users[body.UserID]; !ok {
		matrixError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
		return
	}
	f.notices[body.UserID] = append(f.notices[body.UserID], body.Content.Body)
	reply(w, http.StatusOK, map[string]string{"event_id": "$notice" + strconv.Itoa(len(f.notices[body.UserID]))})
}

func (f *Fake) listDestinations(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := paging(w, r.URL.Query()); !ok {
		return
	}
	reply(w, http.StatusOK, map[string]any{
		"destinations": []map[string]any{
			{"destination": "matrix.org", "retry_last_ts": 0, "retry_interval": 0, "failure_ts": nil, "last_successful_stream_ordering": 42},
			{"destination": "down.example", "retry_last_ts": 1557332397936, "retry_interval": 3000000, "failure_ts": 1557329397936, "last_successful_stream_ordering": nil},
		},
		"total": 2,
	})
}

func (f *Fake) masTokenJSON(t *regToken) map[string]any {
	attrs := map[string]any{
		"token": t.token, "valid": !t.revoked, "usage_limit": t.usesAllowed, "times_used": t.completed,
		"created_at": t.createdAt, "last_used_at": nil, "expires_at": nil, "revoked_at": nil,
	}
	if t.usesAllowed != nil && t.completed >= *t.usesAllowed {
		attrs["valid"] = false
	}
	if t.expiryTime != nil {
		attrs["expires_at"] = time.UnixMilli(*t.expiryTime).UTC().Format(time.RFC3339)
	}
	if t.revoked {
		attrs["revoked_at"] = "2026-01-03T00:00:00Z"
	}
	self := "/api/admin/v1/user-registration-tokens/" + t.ulid
	return map[string]any{"type": "user-registration_token", "id": t.ulid, "attributes": attrs, "links": map[string]string{"self": self}}
}

func (f *Fake) masListTokens(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(f.tokens))
	for id := range f.tokens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, f.masTokenJSON(f.tokens[id]))
	}
	reply(w, http.StatusOK, map[string]any{"meta": map[string]int{"count": len(data)}, "data": data, "links": map[string]any{"self": r.URL.RequestURI(), "next": nil}})
}

func (f *Fake) masCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token      *string `json:"token"`
		UsageLimit *int    `json:"usage_limit"`
		ExpiresAt  *string `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		masError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	token := "mas-" + strconv.Itoa(len(f.tokens)+1)
	if body.Token != nil {
		if _, exists := f.tokens[*body.Token]; exists {
			masError(w, http.StatusConflict, "Token already exists")
			return
		}
		token = *body.Token
	}
	t := &regToken{token: token, usesAllowed: body.UsageLimit, ulid: f.nextULID(), createdAt: "2026-01-02T00:00:00Z"}
	if body.ExpiresAt != nil {
		if at, err := time.Parse(time.RFC3339, *body.ExpiresAt); err == nil {
			ms := at.UnixMilli()
			t.expiryTime = &ms
		}
	}
	f.tokens[token] = t
	reply(w, http.StatusCreated, map[string]any{"data": f.masTokenJSON(t), "links": map[string]string{"self": "/api/admin/v1/user-registration-tokens/" + t.ulid}})
}

func (f *Fake) masTokenAction(w http.ResponseWriter, r *http.Request) {
	var target *regToken
	for _, t := range f.tokens {
		if t.ulid == r.PathValue("id") {
			target = t
		}
	}
	if target == nil {
		masError(w, http.StatusNotFound, "Registration token not found")
		return
	}
	switch r.PathValue("action") {
	case "revoke":
		if target.revoked {
			masError(w, http.StatusBadRequest, "Token is already revoked")
			return
		}
		target.revoked = true
	case "unrevoke":
		target.revoked = false
	default:
		masError(w, http.StatusNotFound, "Not found")
		return
	}
	reply(w, http.StatusOK, map[string]any{"data": f.masTokenJSON(target), "links": map[string]string{"self": "/api/admin/v1/user-registration-tokens/" + target.ulid}})
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
		skipErase, _ := body["skip_erase"].(bool)
		u.deactivated = true
		u.locked = false
		u.erased = u.erased || !skipErase
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
