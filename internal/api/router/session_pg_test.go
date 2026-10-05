package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

// An access token outlives the account state it was issued for. Every request
// re-reads that state, so demotion, logout and deletion take effect at once
// instead of when the token expires.
func TestAccessTokenFollowsTheAccount(t *testing.T) {
	db := storetest.NewDB(t)
	cfg := config.DefaultConfig()
	cfg.Security.JWT.Secret = strings.Repeat("session-test", 4)
	cfg.Security.JWT.AccessTokenTTL = time.Hour
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	app := New(cfg, db, ring, zap.NewNop())
	t.Cleanup(app.Close)

	var userID int64
	if err := db.QueryRow(`INSERT INTO users (username, email, password_hash, role)
		VALUES ('token-follower', 'token-follower@localhost', 'x', 'admin') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })
	const session = "token-follower-session"
	addSession := func() {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO sessions (user_id, session_id, refresh_token_hash, expires_at)
			VALUES ($1, $2, 'x', NOW() + INTERVAL '1 hour')`, userID, session); err != nil {
			t.Fatal(err)
		}
	}
	addSession()

	pair, err := auth.NewJWTManager(cfg.Security.JWT).GenerateTokenPair(userID, "token-follower", "admin", session, "")
	if err != nil {
		t.Fatal(err)
	}
	listUsers := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := listUsers(); code != http.StatusOK {
		t.Fatalf("admin with a live session: %d", code)
	}
	if _, err := db.Exec(`UPDATE users SET role = 'viewer' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if code := listUsers(); code != http.StatusForbidden {
		t.Errorf("after demotion to viewer: %d, want 403", code)
	}
	if _, err := db.Exec(`UPDATE users SET role = 'admin' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE session_id = $1`, session); err != nil {
		t.Fatal(err)
	}
	if code := listUsers(); code != http.StatusUnauthorized {
		t.Errorf("after logout: %d, want 401", code)
	}
	addSession()
	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if code := listUsers(); code != http.StatusUnauthorized {
		t.Errorf("after the account was deleted: %d, want 401", code)
	}
}
