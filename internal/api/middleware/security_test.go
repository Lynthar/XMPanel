package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestIDFor runs one request through the middleware and returns the id the
// handler sees — the value that ends up in the audit row's request_id column.
func requestIDFor(header string) string {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetRequestID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set("X-Request-ID", header)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestRequestID_KeepsWellFormedClientValue(t *testing.T) {
	const id = "trace-abc_123.4"
	if got := requestIDFor(id); got != id {
		t.Errorf("got %q, want %q", got, id)
	}
}

func TestRequestID_GeneratesWhenAbsent(t *testing.T) {
	got := requestIDFor("")
	if len(got) != 32 {
		t.Errorf("generated id = %q, want 32 hex chars", got)
	}
}

// A client value longer than the request_id column makes the audit INSERT
// fail, which used to cost a chain slot; it must never reach storage.
func TestRequestID_ReplacesOverlongClientValue(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := requestIDFor(long)
	if got == long {
		t.Fatal("300-char client id was passed through")
	}
	if len(got) > maxClientRequestIDLen {
		t.Errorf("replacement id length = %d, want <= %d", len(got), maxClientRequestIDLen)
	}
}

func TestRequestID_ReplacesValueWithUnexpectedCharacters(t *testing.T) {
	for _, bad := range []string{"has space", "quote\"", "nl\ninjected", "semi;colon", "é"} {
		if got := requestIDFor(bad); got == bad {
			t.Errorf("%q was passed through", bad)
		}
	}
}

// The response header must carry the same id the handler recorded, or the
// caller cannot correlate its request with the audit row.
func TestRequestID_EchoesTheStoredValue(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetRequestID(r.Context())
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", strings.Repeat("b", 300))
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != seen {
		t.Errorf("header %q != stored %q", got, seen)
	}
}
