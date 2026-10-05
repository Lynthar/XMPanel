package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/config"
)

// http.Error answers text/plain, which the SPA cannot read a message from;
// every rejection here goes through writeError instead.
func TestNoPlainTextResponses(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "http.Error(") {
				t.Errorf("%s:%d answers text/plain: %s", file, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestRejectionsAreLocalizedJSON(t *testing.T) {
	jwt := auth.NewJWTManager(config.JWTConfig{
		Secret:          strings.Repeat("respond-test", 4),
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: time.Hour,
		Issuer:          "xmpanel-test",
	})
	gone := func(context.Context, int64, string) (string, error) { return "", ErrSessionGone }
	pair, err := jwt.GenerateTokenPair(1, "tester", "admin", "session", "")
	if err != nil {
		t.Fatal(err)
	}
	limiter := NewRateLimiter(config.RateLimitConfig{RequestsPerSecond: 1, Burst: 1})
	limited := RateLimit(limiter)(okHandler())
	limited.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	cases := []struct {
		name    string
		handler http.Handler
		bearer  string
		status  int
		want    string
	}{
		{"no token", NewAuthMiddleware(jwt, gone).Authenticate(okHandler()), "", http.StatusUnauthorized, "请先登录"},
		{"session gone", NewAuthMiddleware(jwt, gone).Authenticate(okHandler()), pair.AccessToken, http.StatusUnauthorized, "会话已被撤销"},
		{"no csrf", NewCSRFMiddleware(false).Protect(okHandler()), "", http.StatusForbidden, "安全令牌缺失或已失效，请刷新页面后重试"},
		{"rate limited", limited, "", http.StatusTooManyRequests, "请求过于频繁，请稍后再试"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("Accept-Language", "zh-CN")
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			LocaleMiddleware(tc.handler).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			if got, want := rec.Body.String(), `{"error":"`+tc.want+`"}`+"\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

// A failed login under a fresh name leaves a key behind; once its window has
// passed the key must go, or names nobody logs in with pile up forever.
func TestLoginRateLimiterForgetsExpiredKeys(t *testing.T) {
	const window = 50 * time.Millisecond
	lr := NewLoginRateLimiter(5, window)
	for i := 0; i < 1000; i++ {
		lr.Check(fmt.Sprintf("203.0.113.9:probe-%d", i))
	}
	time.Sleep(3 * window)
	lr.Check("203.0.113.9:fresh")

	lr.mu.Lock()
	defer lr.mu.Unlock()
	if n := len(lr.attempts); n != 1 {
		t.Fatalf("%d keys retained after their window, want only the fresh one", n)
	}
}

// A locked key stays until its lockout ends, or the sweep would lift it early.
func TestLoginRateLimiterKeepsLockouts(t *testing.T) {
	const window = 200 * time.Millisecond
	lr := NewLoginRateLimiter(1, window)
	lr.Check("203.0.113.9:victim")
	time.Sleep(120 * time.Millisecond)
	if allowed, _ := lr.Check("203.0.113.9:victim"); allowed {
		t.Fatal("second attempt should lock the key")
	}
	// The first try is now outside the window while the lockout still runs.
	time.Sleep(130 * time.Millisecond)
	lr.Check("203.0.113.9:other")
	if allowed, _ := lr.Check("203.0.113.9:victim"); allowed {
		t.Fatal("the sweep lifted a lockout that had not ended")
	}
}
