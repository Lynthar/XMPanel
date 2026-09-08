package ejabberd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/store/models"
)

// Fixtures are the upstream result_example values (mod_admin_extra.erl,
// mod_muc_admin.erl) as mod_http_api emits them on API v1 and later: a named
// scalar arrives bare, a list of named tuples as objects keyed by field name.
var upstreamReplies = map[string]string{
	"connected_users_number": `2`,
	"incoming_s2s_number":    `3`,
	"stats":                  `6`,
	"connected_users_info": `[{"jid":"user1@myserver.com/tka","connection":"c2s",
		"ip":"127.0.0.1","port":42656,"priority":8,"node":"ejabberd@localhost",
		"uptime":231,"status":"dnd","resource":"tka","statustext":""}]`,
	"muc_online_rooms":          `["room1@conference.example.com","room2@conference.example.com"]`,
	"get_room_options":          `[{"name":"members_only","value":"true"},{"name":"public","value":"true"}]`,
	"get_room_occupants_number": `7`,
}

// newFixtureAdapter serves upstreamReplies over HTTP and records the JSON
// arguments each command was called with.
func newFixtureAdapter(t *testing.T) (*Adapter, map[string]map[string]string) {
	t.Helper()
	calls := make(map[string]map[string]string)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		command := strings.TrimPrefix(r.URL.Path, "/api/")

		var args map[string]string
		json.NewDecoder(r.Body).Decode(&args)
		calls[command] = args

		body, ok := upstreamReplies[command]
		if !ok {
			t.Errorf("unexpected command %q", command)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	a := NewAdapter(&models.XMPPServer{}, "test-key")
	a.baseURL = srv.URL + "/api"
	return a, calls
}

func TestGetOnlineSessions_ReadsJIDField(t *testing.T) {
	a, _ := newFixtureAdapter(t)

	sessions, err := a.GetOnlineSessions(context.Background())
	if err != nil {
		t.Fatalf("GetOnlineSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}

	got := sessions[0]
	// There is no "user" or "server" key upstream; building the JID from
	// those produced "@/tka" for every session.
	if got.JID != "user1@myserver.com/tka" {
		t.Errorf("JID = %q, want %q", got.JID, "user1@myserver.com/tka")
	}
	if got.Resource != "tka" {
		t.Errorf("Resource = %q, want %q", got.Resource, "tka")
	}
	if got.IPAddress != "127.0.0.1" {
		t.Errorf("IPAddress = %q, want %q", got.IPAddress, "127.0.0.1")
	}
	if got.Priority != 8 {
		t.Errorf("Priority = %d, want 8", got.Priority)
	}
	if got.Status != "dnd" {
		t.Errorf("Status = %q, want %q", got.Status, "dnd")
	}
}

func TestListRooms_SplitsJIDAndFetchesDetails(t *testing.T) {
	a, calls := newFixtureAdapter(t)

	rooms, err := a.ListRooms(context.Background(), "conference.example.com")
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Fatalf("len(rooms) = %d, want 2", len(rooms))
	}

	// muc_online_rooms already returns room@service; the old code appended
	// the domain a second time.
	if rooms[0].JID != "room1@conference.example.com" {
		t.Errorf("JID = %q, want %q", rooms[0].JID, "room1@conference.example.com")
	}
	if rooms[0].Name != "room1" {
		t.Errorf("Name = %q, want %q", rooms[0].Name, "room1")
	}
	if !rooms[0].MembersOnly || !rooms[0].Public {
		t.Errorf("options not applied: members_only=%v public=%v", rooms[0].MembersOnly, rooms[0].Public)
	}
	if rooms[0].Occupants != 7 {
		t.Errorf("Occupants = %d, want 7 (the listing never asked before)", rooms[0].Occupants)
	}

	// get_room_options takes the bare room name plus the service, so a
	// doubled name silently matched nothing.
	if got := calls["get_room_options"]["name"]; got != "room2" {
		t.Errorf("get_room_options name = %q, want the bare room name", got)
	}
	if got := calls["get_room_options"]["service"]; got != "conference.example.com" {
		t.Errorf("get_room_options service = %q", got)
	}
}

func TestGetStats_ParsesBareIntegers(t *testing.T) {
	a, calls := newFixtureAdapter(t)

	stats, err := a.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}

	if stats.OnlineUsers != 2 {
		t.Errorf("OnlineUsers = %d, want 2", stats.OnlineUsers)
	}
	// `stats` returns {stat, integer}, which v1+ unwraps to a bare number
	// exactly like the two counters around it; the object parse read 0.
	if stats.RegisteredUsers != 6 {
		t.Errorf("RegisteredUsers = %d, want 6", stats.RegisteredUsers)
	}
	if stats.S2SConnections != 3 {
		t.Errorf("S2SConnections = %d, want 3", stats.S2SConnections)
	}
	if got := calls["stats"]["name"]; got != "registeredusers" {
		t.Errorf("stats name = %q, want registeredusers", got)
	}
}

func TestGetRoom_UsesBareNameAndOccupants(t *testing.T) {
	a, calls := newFixtureAdapter(t)

	room, err := a.GetRoom(context.Background(), "room1", "conference.example.com")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if room.JID != "room1@conference.example.com" {
		t.Errorf("JID = %q", room.JID)
	}
	if room.Occupants != 7 {
		t.Errorf("Occupants = %d, want 7", room.Occupants)
	}
	if got := calls["get_room_occupants_number"]["name"]; got != "room1" {
		t.Errorf("occupants name = %q, want room1", got)
	}
}
