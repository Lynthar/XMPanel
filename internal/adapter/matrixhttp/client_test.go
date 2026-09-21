package matrixhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		status  int
		errcode string
		want    adapter.Kind
	}{
		{401, "M_UNKNOWN_TOKEN", adapter.Unauthorized},
		{401, "", adapter.Unauthorized},
		{403, "M_FORBIDDEN", adapter.Forbidden},
		{404, "M_NOT_FOUND", adapter.NotFound},
		{404, "M_UNRECOGNIZED", adapter.NotSupported},
		{404, "", adapter.Upstream},
		{400, "M_USER_IN_USE", adapter.Conflict},
		{400, "M_INVALID_USERNAME", adapter.Invalid},
		{400, "M_INVALID_PARAM", adapter.Invalid},
		{409, "", adapter.Conflict},
		{429, "M_LIMIT_EXCEEDED", adapter.RateLimited},
		{500, "M_UNKNOWN", adapter.Upstream},
		{502, "", adapter.Upstream},
	} {
		if got := Classify(tc.status, tc.errcode); got != tc.want {
			t.Errorf("Classify(%d, %q) = %d, want %d", tc.status, tc.errcode, got, tc.want)
		}
	}
}

// Call carries the token unless the request is anonymous, decodes JSON,
// keeps the errcode and retry hint of a failure, and reports a server it
// could not reach as Unreachable.
func TestCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anon":
			if r.Header.Get("Authorization") != "" {
				http.Error(w, "token sent", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/auth":
			if r.Header.Get("Authorization") != "Bearer secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN","error":"Unknown token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/limited":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errcode":"M_LIMIT_EXCEEDED","error":"Too fast","retry_after_ms":1500}`))
		case "/secret/tok":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"No such token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Token: "secret"}
	ctx := context.Background()

	var out struct {
		OK bool `json:"ok"`
	}
	if _, err := c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/anon", Anonymous: true}, &out); err != nil || !out.OK {
		t.Errorf("anonymous call: %v %+v", err, out)
	}
	if status, err := c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/auth"}, nil); err != nil || status != http.StatusOK {
		t.Errorf("authenticated call: %d %v", status, err)
	}
	c.Token = "wrong"
	_, err := c.Call(ctx, Request{Op: "t", Resource: "r", Method: http.MethodGet, Path: "/auth"}, nil)
	failure, ok := adapter.AsError(err)
	if !ok || failure.Kind != adapter.Unauthorized || failure.Code != "M_UNKNOWN_TOKEN" || failure.Op != "t" || failure.Resource != "r" {
		t.Errorf("bad token: %+v", failure)
	}
	_, err = c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/limited"}, nil)
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.RateLimited || failure.RetryAfter != 1500*time.Millisecond {
		t.Errorf("rate limited: %+v", failure)
	}
	_, err = c.Call(ctx, Request{Op: "t", Method: http.MethodDelete, Path: "/secret/tok", Label: "/secret/{token}"}, nil)
	if err == nil || !strings.Contains(err.Error(), "/secret/{token}") || strings.Contains(err.Error(), "/secret/tok") {
		t.Errorf("label must replace the path in the message: %v", err)
	}
	_, err = c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/missing"}, nil)
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Upstream || failure.Status != http.StatusNotFound {
		t.Errorf("404 without a Matrix body: %+v", failure)
	}

	c.Kind = func(status int, errcode, message string) adapter.Kind {
		if message == "No such token" {
			return adapter.Conflict
		}
		return Classify(status, errcode)
	}
	if _, err := c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/secret/tok"}, nil); !isKind(err, adapter.Conflict) {
		t.Errorf("Kind hook not applied: %v", err)
	}

	srv.Close()
	_, err = c.Call(ctx, Request{Op: "t", Method: http.MethodGet, Path: "/auth"}, nil)
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.Unreachable {
		t.Errorf("closed server: %+v", failure)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.Call(cancelled, Request{Op: "t", Method: http.MethodGet, Path: "/auth"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must stay recognisable: %v", err)
	}
}

func isKind(err error, kind adapter.Kind) bool {
	failure, ok := adapter.AsError(err)
	return ok && failure.Kind == kind
}
