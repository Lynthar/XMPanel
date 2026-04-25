package models

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// makeEntry builds a deterministic AuditLogEntry for hashing tests.
func makeEntry() *AuditLogEntry {
	return &AuditLogEntry{
		Username:     "alice",
		Action:       AuditActionUserCreate,
		ResourceType: ResourceTypeUser,
		ResourceID:   "42",
		Details:      map[string]interface{}{"role": "admin"},
		IPAddress:    "10.0.0.1",
		UserAgent:    "Go-test/1.0",
		RequestID:    "req-1",
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	e := makeEntry()
	ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h1 := e.ComputeHash(1, ts, "")
	h2 := e.ComputeHash(1, ts, "")
	if h1 != h2 {
		t.Errorf("non-deterministic hash: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("hash length=%d, want 64 (sha256 hex)", len(h1))
	}
}

func TestComputeHash_DiffersOnAnyChange(t *testing.T) {
	ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	base := makeEntry().ComputeHash(1, ts, "")

	// Different ID
	if makeEntry().ComputeHash(2, ts, "") == base {
		t.Error("hash unchanged when id changed")
	}
	// Different timestamp
	if makeEntry().ComputeHash(1, ts.Add(time.Second), "") == base {
		t.Error("hash unchanged when timestamp changed")
	}
	// Different prevHash
	if makeEntry().ComputeHash(1, ts, "prev") == base {
		t.Error("hash unchanged when prevHash changed")
	}
	// Different field
	tampered := makeEntry()
	tampered.Username = "eve"
	if tampered.ComputeHash(1, ts, "") == base {
		t.Error("hash unchanged when username changed")
	}
}

// buildChain constructs N audit logs in sequence with valid hashes, mirroring
// what AuditService.Log would produce.
func buildChain(t *testing.T, n int) []AuditLog {
	t.Helper()
	logs := make([]AuditLog, n)
	prevHash := ""
	for i := 0; i < n; i++ {
		entry := &AuditLogEntry{
			Username: "alice",
			Action:   AuditActionLogin,
		}
		ts := time.Date(2026, 4, 25, 12, i, 0, 0, time.UTC)
		id := int64(i + 1)
		hash := entry.ComputeHash(id, ts, prevHash)

		detailsJSON, _ := json.Marshal(entry.Details)
		logs[i] = AuditLog{
			ID:        id,
			Username:  entry.Username,
			Action:    entry.Action,
			Details:   sql.NullString{String: string(detailsJSON), Valid: detailsJSON != nil},
			PrevHash:  sql.NullString{String: prevHash, Valid: i > 0},
			Hash:      hash,
			CreatedAt: ts,
		}
		prevHash = hash
	}
	return logs
}

func TestVerifyChain_Empty(t *testing.T) {
	ok, broken, err := VerifyChain(nil)
	if err != nil || !ok || broken != 0 {
		t.Errorf("empty chain: ok=%v broken=%d err=%v", ok, broken, err)
	}
}

func TestVerifyChain_ValidChain(t *testing.T) {
	logs := buildChain(t, 5)
	ok, broken, err := VerifyChain(logs)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !ok || broken != -1 {
		t.Errorf("valid chain reported broken: ok=%v broken=%d", ok, broken)
	}
}

func TestVerifyChain_DetectsTamperedField(t *testing.T) {
	logs := buildChain(t, 5)
	// Tamper with the username on entry 2 without recomputing the hash.
	logs[2].Username = "mallory"

	ok, broken, err := VerifyChain(logs)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if ok {
		t.Error("VerifyChain accepted a tampered chain")
	}
	if broken != 2 {
		t.Errorf("broken index = %d, want 2", broken)
	}
}

func TestVerifyChain_DetectsTamperedHash(t *testing.T) {
	logs := buildChain(t, 3)
	logs[1].Hash = "0000000000000000000000000000000000000000000000000000000000000000"
	ok, broken, _ := VerifyChain(logs)
	if ok {
		t.Error("VerifyChain accepted bogus hash")
	}
	if broken != 1 {
		t.Errorf("broken index = %d, want 1", broken)
	}
}
