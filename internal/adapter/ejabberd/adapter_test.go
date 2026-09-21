package ejabberd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/adaptertest"
)

const (
	fakeDomain  = "example.com"
	fakeService = "conference.example.com"
	fakeToken   = "ejabberd-oauth-token"
)

// fakeEjabberd answers mod_http_api commands the way ejabberd 23.10 did in
// the smoke run: a POST must carry a JSON body, a named integer result comes
// wrapped as {"name": N} while a rescode is bare, get_room_options is one
// object and answers {} for a missing room, and command errors carry
// {"status","code","message"} with 409 for conflict, 404 for not_found and
// 500 for the rest.
type fakeEjabberd struct {
	mu       sync.Mutex
	fail     int
	users    map[string]bool
	sessions map[string]adapter.Session
	rooms    map[string]map[string]string
	calls    map[string]map[string]string
}

func newFakeEjabberd() *fakeEjabberd { return &fakeEjabberd{} }

func (f *fakeEjabberd) Reset() adaptertest.Population {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = map[string]bool{"alice": true, "bob": true}
	f.sessions = map[string]adapter.Session{}
	f.rooms = map[string]map[string]string{
		"room1@" + fakeService: {"title": "Room One", "public": "true", "persistent": "true", "members_only": "false"},
		"room2@" + fakeService: {"title": "", "public": "false", "persistent": "false", "members_only": "true"},
	}
	f.calls = map[string]map[string]string{}
	pop := adaptertest.Population{
		Accounts: []string{"alice@example.com", "bob@example.com"},
		Rooms:    []string{"room1@" + fakeService, "room2@" + fakeService},
	}
	for _, s := range []adapter.Session{
		{ID: "alice@example.com/tka", AccountID: "alice@example.com"},
		{ID: "bob@example.com/phone", AccountID: "bob@example.com"},
		{ID: "bob@example.com/laptop", AccountID: "bob@example.com"},
	} {
		f.sessions[s.ID] = s
		pop.Sessions = append(pop.Sessions, s)
	}
	return pop
}

func (f *fakeEjabberd) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = status
}

func commandError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "code": code, "message": message})
}

func (f *fakeEjabberd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != 0 {
		w.WriteHeader(f.fail)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	command := strings.TrimPrefix(r.URL.Path, "/api/")
	args := map[string]string{}
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.calls[command] = args
	reply := func(v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	jid := args["user"] + "@" + args["host"]
	roomID := args["name"] + "@" + args["service"]

	switch command {
	case "status":
		reply("The node ejabberd@localhost is started with status: started\nejabberd 24.02 is running in that node")
	case "registered_vhosts":
		reply([]string{fakeDomain})
	case "connected_users_number":
		reply(map[string]int{"num_sessions": len(f.sessions)})
	case "incoming_s2s_number":
		reply(map[string]int{"s2s_incoming": 3})
	case "stats":
		switch args["name"] {
		case "registeredusers":
			reply(map[string]int{"stat": len(f.users)})
		case "uptimeseconds":
			reply(map[string]int{"stat": 4242})
		default:
			commandError(w, http.StatusInternalServerError, 0, "Unknown stat")
		}
	case "registered_users":
		out := []string{}
		for u := range f.users {
			out = append(out, u)
		}
		reply(out)
	case "check_account":
		if f.users[args["user"]] {
			reply(0)
		} else {
			reply(1)
		}
	case "register":
		if f.users[args["user"]] {
			commandError(w, http.StatusConflict, 10090, "User "+jid+" already registered")
			return
		}
		f.users[args["user"]] = true
		reply("User " + jid + " successfully registered")
	case "unregister":
		delete(f.users, args["user"])
		reply("Success")
	case "change_password":
		if !f.users[args["user"]] {
			commandError(w, http.StatusNotFound, 10007, "unknown_user")
			return
		}
		reply(0)
	case "connected_users_info":
		out := []map[string]interface{}{}
		for _, s := range f.sessions {
			_, resource, _ := strings.Cut(s.ID, "/")
			out = append(out, map[string]interface{}{
				"jid": s.ID, "connection": "c2s", "ip": "127.0.0.1", "port": 42656, "priority": 8,
				"node": "ejabberd@localhost", "uptime": 231, "status": "dnd", "resource": resource, "statustext": "",
			})
		}
		reply(out)
	case "user_sessions_info":
		out := []map[string]interface{}{}
		for _, s := range f.sessions {
			if s.AccountID != jid {
				continue
			}
			_, resource, _ := strings.Cut(s.ID, "/")
			out = append(out, map[string]interface{}{
				"connection": "c2s", "ip": "127.0.0.1", "port": 42656, "priority": 8,
				"node": "ejabberd@localhost", "uptime": 231, "status": "dnd", "resource": resource, "statustext": "",
			})
		}
		reply(out)
	case "kick_session":
		id := jid + "/" + args["resource"]
		if _, ok := f.sessions[id]; !ok {
			commandError(w, http.StatusInternalServerError, 0, "Session not found")
			return
		}
		delete(f.sessions, id)
		reply(0)
	case "kick_user":
		n := 0
		for id, s := range f.sessions {
			if s.AccountID == jid {
				delete(f.sessions, id)
				n++
			}
		}
		reply(n)
	case "muc_online_rooms":
		out := []string{}
		for id := range f.rooms {
			if args["service"] == "global" || strings.HasSuffix(id, "@"+args["service"]) {
				out = append(out, id)
			}
		}
		reply(out)
	case "get_room_options":
		room, ok := f.rooms[roomID]
		if !ok {
			reply(map[string]string{})
			return
		}
		reply(room)
	case "get_room_occupants_number":
		reply(map[string]int{"occupants": 7})
	case "create_room":
		if _, ok := f.rooms[roomID]; ok {
			commandError(w, http.StatusInternalServerError, 0, "Room already exists")
			return
		}
		f.rooms[roomID] = map[string]string{"title": "", "public": "false", "persistent": "false", "members_only": "false", "moderated": "true"}
		reply(0)
	case "change_room_option":
		room, ok := f.rooms[roomID]
		if !ok {
			commandError(w, http.StatusInternalServerError, 0, "Room not found")
			return
		}
		room[args["option"]] = args["value"]
		reply(0)
	case "destroy_room":
		if _, ok := f.rooms[roomID]; !ok {
			commandError(w, http.StatusInternalServerError, 0, "Room not found")
			return
		}
		delete(f.rooms, roomID)
		reply(0)
	default:
		commandError(w, http.StatusNotFound, 32, "Command not found")
	}
}

func newAdapter(endpoint string) adapter.Adapter {
	return New(adapter.ServerConfig{
		Protocol: adapter.ProtocolXMPP, Impl: adapter.ImplEjabberd,
		Endpoint: endpoint, Domain: fakeDomain,
		Creds: adapter.Credentials{Kind: adapter.CredentialsBearer, Token: fakeToken},
	})
}

func TestContract(t *testing.T) {
	adaptertest.Run(t, adaptertest.Config{
		Protocol: adapter.ProtocolXMPP,
		Impl:     adapter.ImplEjabberd,
		Domain:   fakeDomain,
		Upstream: newFakeEjabberd(),
		New:      newAdapter,
		Expected: capabilities,
	})
}

// roomOptions must also read the API v1+ list-of-pairs encoding, which the
// unversioned URL selects on newer releases.
func TestRoomOptionsAcceptPairs(t *testing.T) {
	options, err := roomOptions([]byte(`[{"name":"public","value":"true"},{"name":"title","value":"T"}]`))
	if err != nil || options["public"] != "true" || options["title"] != "T" {
		t.Fatalf("pairs = %v, %v", options, err)
	}
	if _, err := roomOptions([]byte(`"nope"`)); err == nil {
		t.Fatal("a string is not an options encoding")
	}
}

// Upstream argument names are part of the contract with mod_admin_extra and
// mod_muc_admin: get_room_options keeps "name", kick_user sends no reason.
func TestCommandArguments(t *testing.T) {
	fake := newFakeEjabberd()
	fake.Reset()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newAdapter(srv.URL)
	ctx := context.Background()

	room, err := a.GetRoom(ctx, "room1@"+fakeService)
	if err != nil {
		t.Fatal(err)
	}
	if room.Name != "Room One" || room.Members != 7 || !room.Public || room.XMPP == nil || !room.XMPP.Persistent {
		t.Fatalf("room = %+v", room)
	}
	if got := fake.calls["get_room_options"]; got["name"] != "room1" || got["service"] != fakeService {
		t.Fatalf("get_room_options args = %v", got)
	}
	if err := a.TerminateAccountSessions(ctx, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls["kick_user"]; got["user"] != "bob" || got["host"] != fakeDomain || got["reason"] != "" {
		t.Fatalf("kick_user args = %v", got)
	}
	info, err := a.Probe(ctx)
	if err != nil || info.Version != "24.02" {
		t.Fatalf("version from status text = %+v, %v", info, err)
	}
	stats, err := a.Stats(ctx)
	if err != nil || stats.UptimeSeconds == nil || *stats.UptimeSeconds != 4242 || stats.S2SConnections == nil || *stats.S2SConnections != 3 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	page, err := a.ListRooms(ctx, adapter.ListQuery{})
	if err != nil || fake.calls["muc_online_rooms"]["service"] != "global" || len(page.Items) != 2 {
		t.Fatalf("rooms across services: %+v %v %v", page, fake.calls["muc_online_rooms"], err)
	}
}
