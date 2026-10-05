package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// testUser inserts a panel user and removes it when the test ends.
func testUser(t *testing.T, db *store.DB, name, role string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, 'x', $3) RETURNING id`, name, name+"@localhost", role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, id) })
	return id
}

// as runs req as the given user, the way Authenticate leaves the context.
func as(req *http.Request, id int64, name, role string) *http.Request {
	return req.WithContext(middleware.WithClaims(req.Context(), &auth.Claims{UserID: id, Username: name, Role: role}))
}

// Every row past the cap would be dropped; the export refuses instead of
// handing over a file that looks complete.
func TestAuditExport_RefusesMoreRowsThanItCanHold(t *testing.T) {
	db := newTestDB(t)
	_, h := newTestAudit(t, db)
	if _, err := db.Exec(`INSERT INTO audit_logs (username, action, resource_type, hash)
		SELECT 'bulk', 'auth.login', 'user', md5(g::text) FROM generate_series(1, $1) g`, maxExportRows+1); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Export(rec, httptest.NewRequest(http.MethodGet, "/audit/export", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("over the cap: %d %q", rec.Code, rec.Body.String())
	}

	if _, err := db.Exec(`DELETE FROM audit_logs WHERE id = (SELECT MIN(id) FROM audit_logs)`); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Export(rec, httptest.NewRequest(http.MethodGet, "/audit/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("at the cap: %d", rec.Code)
	}
}

// A login under a name longer than any account's is still a failed login
// worth recording; the row used to be lost to the column width.
func TestLogin_RecordsAnOverlongUsername(t *testing.T) {
	db := newTestDB(t)
	svc, _ := newTestAudit(t, db)
	h := NewAuthHandler(db, nil, nil, nil, middleware.NewLoginRateLimiter(5, time.Minute), svc, time.Hour, false, zap.NewNop())

	name := strings.Repeat("名", 300)
	body, _ := json.Marshal(map[string]string{"username": name, "password": "whatever"})
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(body))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}

	var stored string
	if err := db.QueryRow(`SELECT username FROM audit_logs WHERE action = $1`, models.AuditActionLoginFailed).Scan(&stored); err != nil {
		t.Fatalf("no audit row for the attempt: %v", err)
	}
	if utf8.RuneCountInString(stored) != maxUsernameLength || !strings.HasPrefix(name, stored) {
		t.Errorf("stored name has %d runes, want the first %d", utf8.RuneCountInString(stored), maxUsernameLength)
	}
}

// Starting setup again would swap the secret the user's authenticator holds
// while MFA stays on, so the next login could not pass.
func TestSetupMFA_RefusesWhileEnabled(t *testing.T) {
	db := newTestDB(t)
	id := testUser(t, db, "mfa-holder", "viewer")
	if _, err := db.Exec(`UPDATE users SET mfa_enabled = TRUE, mfa_secret = 'KEEPTHISSECRET' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(db, nil, nil, nil, nil, nil, time.Hour, false, zap.NewNop())

	rec := httptest.NewRecorder()
	h.SetupMFA(rec, as(httptest.NewRequest(http.MethodPost, "/", nil), id, "mfa-holder", "viewer"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	var secret string
	if err := db.QueryRow(`SELECT mfa_secret FROM users WHERE id = $1`, id).Scan(&secret); err != nil {
		t.Fatal(err)
	}
	if secret != "KEEPTHISSECRET" {
		t.Errorf("secret replaced with %q", secret)
	}
}

func TestResetMFA(t *testing.T) {
	db := newTestDB(t)
	svc, _ := newTestAudit(t, db)
	h := NewUserHandler(db, nil, nil, nil, svc, zap.NewNop())
	admin := testUser(t, db, "mfa-resetter", "admin")
	target := testUser(t, db, "mfa-lost", "operator")
	super := testUser(t, db, "mfa-super", "superadmin")
	if _, err := db.Exec(`UPDATE users SET mfa_enabled = TRUE, mfa_secret = 'S', recovery_codes = '[]' WHERE id IN ($1, $2)`, target, super); err != nil {
		t.Fatal(err)
	}
	reset := func(id, caller int64, role string) int {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		rec := httptest.NewRecorder()
		h.ResetMFA(rec, as(req, caller, "caller", role))
		return rec.Code
	}

	if code := reset(super, admin, "admin"); code != http.StatusForbidden {
		t.Errorf("admin resetting a superadmin: %d, want 403", code)
	}
	if code := reset(admin, admin, "admin"); code != http.StatusBadRequest {
		t.Errorf("resetting oneself: %d, want 400", code)
	}
	if code := reset(target, admin, "admin"); code != http.StatusOK {
		t.Fatalf("reset: %d", code)
	}
	var enabled bool
	var secret, codes *string
	if err := db.QueryRow(`SELECT mfa_enabled, mfa_secret, recovery_codes FROM users WHERE id = $1`, target).Scan(&enabled, &secret, &codes); err != nil {
		t.Fatal(err)
	}
	if enabled || secret != nil || codes != nil {
		t.Errorf("after reset: enabled=%v secret=%v codes=%v", enabled, secret, codes)
	}
	if code := reset(target, admin, "admin"); code != http.StatusConflict {
		t.Errorf("second reset: %d, want 409", code)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = $1 AND resource_id = $2`,
		models.AuditActionUserMFAReset, strconv.FormatInt(target, 10)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d audit rows for one reset", rows)
	}
}

// A role change records both ends; "fields: [role]" alone cannot say whether
// someone was promoted or demoted.
func TestUpdate_AuditsTheRoleChange(t *testing.T) {
	db := newTestDB(t)
	svc, _ := newTestAudit(t, db)
	h := NewUserHandler(db, nil, nil, nil, svc, zap.NewNop())
	admin := testUser(t, db, "role-changer", "admin")
	target := testUser(t, db, "role-changed", "operator")

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"role":"viewer"}`))
	req.SetPathValue("id", strconv.FormatInt(target, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, as(req, admin, "role-changer", "admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var from, to string
	if err := db.QueryRow(`SELECT details->>'role_from', details->>'role_to' FROM audit_logs WHERE action = $1`,
		models.AuditActionUserUpdate).Scan(&from, &to); err != nil {
		t.Fatal(err)
	}
	if from != "operator" || to != "viewer" {
		t.Errorf("audited %q -> %q, want operator -> viewer", from, to)
	}
}
