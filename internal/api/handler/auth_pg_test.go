package handler

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

type refreshFixture struct {
	db      *store.DB
	h       *AuthHandler
	session string
}

// newRefreshFixture logs a user in by hand: one session row holding the hash
// of the refresh token it returns.
func newRefreshFixture(t *testing.T) (*refreshFixture, string) {
	t.Helper()
	db := newTestDB(t)
	var userID int64
	err := db.QueryRow(`INSERT INTO users (username, email, password_hash, role) VALUES ('refresh-tester', 'refresh-tester@localhost', 'x', 'viewer')
		ON CONFLICT (username) DO UPDATE SET locked_until = NULL RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })

	jwt := auth.NewJWTManager(config.JWTConfig{
		Secret:          "test-secret-key-at-least-32-chars-long-for-hs256",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: time.Hour,
		Issuer:          "xmpanel-test",
	})
	f := &refreshFixture{db: db, session: "refresh-test-session"}
	pair, err := jwt.GenerateTokenPair(userID, "refresh-tester", "viewer", f.session, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (user_id, session_id, refresh_token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, f.session, crypto.HashToken(pair.RefreshToken), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.h = NewAuthHandler(db, jwt, nil, nil, nil, nil, time.Hour, false, zap.NewNop())
	return f, pair.RefreshToken
}

// refresh presents token as the browser would and returns the status and the
// rotated refresh cookie, if one was set.
func (f *refreshFixture) refresh(token string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: token})
	rec := httptest.NewRecorder()
	f.h.Refresh(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshCookieName && c.MaxAge > 0 {
			return rec.Code, c.Value
		}
	}
	return rec.Code, ""
}

func (f *refreshFixture) storedHash(t *testing.T) (string, bool) {
	t.Helper()
	var hash string
	err := f.db.QueryRow(`SELECT refresh_token_hash FROM sessions WHERE session_id = $1`, f.session).Scan(&hash)
	if err != nil {
		return "", false
	}
	return hash, true
}

func TestRefresh_ReplayedTokenRevokesTheSession(t *testing.T) {
	f, first := newRefreshFixture(t)

	code, second := f.refresh(first)
	if code != http.StatusOK || second == "" {
		t.Fatalf("first refresh: code = %d, cookie = %q", code, second)
	}
	if code, _ := f.refresh(first); code != http.StatusUnauthorized {
		t.Errorf("replayed token: code = %d, want 401", code)
	}
	if _, ok := f.storedHash(t); ok {
		t.Error("session survived a replayed refresh token")
	}
	if code, _ := f.refresh(second); code != http.StatusUnauthorized {
		t.Errorf("rotated token after the replay: code = %d, want 401", code)
	}
}

// One token presented concurrently rotates at most once, and a session that
// survives holds the hash of the cookie that rotation handed out.
func TestRefresh_ConcurrentUseRotatesAtMostOnce(t *testing.T) {
	f, token := newRefreshFixture(t)

	const callers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, cookie := f.refresh(token); code == http.StatusOK {
				mu.Lock()
				winners = append(winners, cookie)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(winners) > 1 {
		t.Fatalf("%d concurrent refreshes of one token succeeded, want at most 1", len(winners))
	}
	if hash, ok := f.storedHash(t); ok && (len(winners) == 0 || hash != crypto.HashToken(winners[0])) {
		t.Error("the session outlived the race holding a hash no issued cookie matches")
	}
}
