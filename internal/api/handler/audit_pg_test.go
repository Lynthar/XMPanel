package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// These need a real PostgreSQL: advisory locking, sequence allocation and NULL
// handling have no meaning without one. Set XMPANEL_TEST_DSN to a throwaway
// database to run them; otherwise they skip and `go test ./...` stays green.
func newTestDB(t *testing.T) *store.DB {
	t.Helper()

	dsn := os.Getenv("XMPANEL_TEST_DSN")
	if dsn == "" {
		t.Skip("XMPANEL_TEST_DSN not set; skipping PostgreSQL integration test")
	}

	db, err := store.NewDB(config.DatabaseConfig{DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE audit_logs RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestAudit(t *testing.T, db *store.DB) (*AuditService, *AuditHandler) {
	t.Helper()
	logger := zap.NewNop()
	return NewAuditService(db, logger), NewAuditHandler(db, logger)
}

// verifyChain calls the endpoint the UI's button calls and returns its answer.
func verifyChain(t *testing.T, h *AuditHandler) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Verify(rec, httptest.NewRequest(http.MethodGet, "/audit/verify", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("verify: %v", err)
	}
	return result
}

func TestAuditChain_ConcurrentWritesStayLinked(t *testing.T) {
	db := newTestDB(t)
	svc, h := newTestAudit(t, db)

	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			errs <- svc.Log(&models.AuditLogEntry{
				Username:  fmt.Sprintf("user%02d", n),
				Action:    models.AuditActionLogin,
				IPAddress: "10.0.0.1",
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	result := verifyChain(t, h)
	if result["valid"] != true {
		t.Errorf("chain invalid after %d concurrent writes: %v", writers, result)
	}
	if got := result["records_checked"]; got != float64(writers) {
		t.Errorf("records_checked = %v, want %d", got, writers)
	}
}

// A failed INSERT consumes a sequence value. MAX(id)+1 then guessed low and
// every row written afterwards hashed against an id it did not have.
func TestAuditChain_SurvivesABurntSequenceValue(t *testing.T) {
	db := newTestDB(t)
	svc, h := newTestAudit(t, db)

	if err := svc.Log(&models.AuditLogEntry{Username: "first", Action: models.AuditActionLogin}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	// A rejected write: request_id is VARCHAR(255).
	err := svc.Log(&models.AuditLogEntry{
		Username:  "overlong",
		Action:    models.AuditActionLoginFailed,
		RequestID: strings.Repeat("x", 300),
	})
	if err == nil {
		t.Fatal("expected the overlong request_id to be rejected")
	}

	if err := svc.Log(&models.AuditLogEntry{Username: "third", Action: models.AuditActionLogin}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	result := verifyChain(t, h)
	if result["valid"] != true {
		t.Errorf("chain invalid after a burnt sequence value: %v", result)
	}
	if got := result["records_checked"]; got != float64(2) {
		t.Errorf("records_checked = %v, want 2", got)
	}
}

func TestAuditChain_VerifyDetectsADeletedRow(t *testing.T) {
	db := newTestDB(t)
	svc, h := newTestAudit(t, db)

	for i := 0; i < 5; i++ {
		if err := svc.Log(&models.AuditLogEntry{
			Username: fmt.Sprintf("user%d", i),
			Action:   models.AuditActionLogin,
		}); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}
	if result := verifyChain(t, h); result["valid"] != true {
		t.Fatalf("freshly written chain reported broken: %v", result)
	}

	var middleID int64
	if err := db.QueryRow(`SELECT id FROM audit_logs ORDER BY id OFFSET 2 LIMIT 1`).Scan(&middleID); err != nil {
		t.Fatalf("pick row: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_logs WHERE id = $1`, middleID); err != nil {
		t.Fatalf("delete row: %v", err)
	}

	result := verifyChain(t, h)
	if result["valid"] != false {
		t.Errorf("a removed record left the chain reported valid: %v", result)
	}
	if got := result["broken_at_index"]; got != float64(2) {
		t.Errorf("broken_at_index = %v, want 2", got)
	}
}

// Events logged without details store SQL NULL. The CSV export used to scan
// that column into a plain string, fail, and skip the row — losing exactly
// the password changes and MFA switches an auditor is looking for.
func TestAuditExport_IncludesRowsWithNullDetails(t *testing.T) {
	db := newTestDB(t)
	svc, h := newTestAudit(t, db)

	if err := svc.Log(&models.AuditLogEntry{
		Username: "alice", Action: models.AuditActionPasswordChange,
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := svc.Log(&models.AuditLogEntry{
		Username: "bob", Action: models.AuditActionMFADisabled,
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := svc.Log(&models.AuditLogEntry{
		Username: "carol", Action: models.AuditActionUserCreate,
		Details: map[string]interface{}{"role": "viewer"},
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	var nullDetails int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE details IS NULL`).Scan(&nullDetails); err != nil {
		t.Fatalf("count: %v", err)
	}
	if nullDetails != 2 {
		t.Fatalf("rows with NULL details = %d, want 2", nullDetails)
	}

	rec := httptest.NewRecorder()
	h.Export(rec, httptest.NewRequest(http.MethodGet, "/audit/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: code = %d", rec.Code)
	}

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("CSV has %d lines (header + rows), want 4", len(records))
	}

	usernames := map[string]bool{}
	for _, row := range records[1:] {
		usernames[row[1]] = true
	}
	for _, want := range []string{"alice", "bob", "carol"} {
		if !usernames[want] {
			t.Errorf("%q missing from the export", want)
		}
	}
}
