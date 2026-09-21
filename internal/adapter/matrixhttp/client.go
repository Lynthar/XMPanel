// Package matrixhttp is the HTTP client the Matrix adapters share: one bearer
// token against one base URL, JSON in and out, and every failure turned into
// a *adapter.Error whose Kind follows the Matrix errcode.
package matrixhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Request describes one call. Anonymous omits the token: a server delegating
// authentication validates any token presented, so an unauthenticated probe
// step must not carry one or a bad token fails at the wrong step.
type Request struct {
	Op, Resource, Method, Path string
	Query                      url.Values
	Body                       any
	Anonymous                  bool
	Label                      string // shown instead of Path in error text when Path carries a secret
}

func (r Request) Shown() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Path
}

// Client performs requests for one server. Kind, when set, decides the error
// kind from the status, errcode and message instead of Classify alone.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Token   string
	Kind    func(status int, errcode, message string) adapter.Kind
}

// Call performs one request and maps a failure to *adapter.Error. A 404 that
// carries no errcode did not come from the homeserver and is an upstream
// error; a cancelled context stays recognisable through errors.Is.
func (c *Client) Call(ctx context.Context, req Request, out any) (status int, err error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: req.Op, Resource: req.Resource}
	defer func() {
		if err != nil {
			failure.Err = err
			err = failure
		}
	}()

	target := c.BaseURL + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}
	var payload []byte
	if req.Body != nil {
		if payload, err = json.Marshal(req.Body); err != nil {
			return 0, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}
	header := http.Header{"Accept": {"application/json"}}
	if !req.Anonymous {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	if req.Body != nil {
		header.Set("Content-Type", "application/json")
	}
	status, respBody, err := Transport(ctx, c.HTTP, req.Method, target, header, payload)
	if err != nil {
		if status == 0 {
			failure.Kind = adapter.Unreachable
			return 0, fmt.Errorf("failed to connect to server: %w", err)
		}
		failure.Status = status
		return status, fmt.Errorf("failed to read response: %w", err)
	}
	failure.Status = status
	if status >= 200 && status < 300 {
		if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return status, fmt.Errorf("unexpected response body: %w", err)
			}
		}
		return status, nil
	}

	var detail struct {
		Errcode      string `json:"errcode"`
		Error        string `json:"error"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	}
	_ = json.Unmarshal(respBody, &detail)
	failure.Code = detail.Errcode
	if c.Kind != nil {
		failure.Kind = c.Kind(status, detail.Errcode, detail.Error)
	} else {
		failure.Kind = Classify(status, detail.Errcode)
	}
	if failure.Kind == adapter.RateLimited && detail.RetryAfterMS > 0 {
		failure.RetryAfter = time.Duration(detail.RetryAfterMS) * time.Millisecond
	}
	message := detail.Error
	if message == "" {
		message = Snippet(string(respBody))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return status, fmt.Errorf("%s %s: %s", req.Method, req.Shown(), message)
}

// Classify maps a status and Matrix errcode to a Kind. M_UNRECOGNIZED is an
// endpoint the server does not serve, which is NotSupported, not NotFound.
func Classify(status int, errcode string) adapter.Kind {
	switch status {
	case http.StatusUnauthorized:
		return adapter.Unauthorized
	case http.StatusForbidden:
		return adapter.Forbidden
	case http.StatusNotFound:
		switch errcode {
		case "M_NOT_FOUND":
			return adapter.NotFound
		case "M_UNRECOGNIZED":
			return adapter.NotSupported
		}
		return adapter.Upstream
	case http.StatusConflict:
		return adapter.Conflict
	case http.StatusBadRequest:
		if errcode == "M_USER_IN_USE" {
			return adapter.Conflict
		}
		return adapter.Invalid
	case http.StatusTooManyRequests:
		return adapter.RateLimited
	}
	return adapter.Upstream
}

// Transport performs one HTTP exchange and returns the status and body. A
// status of 0 with an error means the server was not reached; the cause is
// kept so a cancelled context stays recognisable through errors.Is.
func Transport(ctx context.Context, client *http.Client, method, target string, header http.Header, body []byte) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return 0, nil, err
	}
	for key, values := range header {
		req.Header[key] = values
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// Snippet keeps the start of a non-JSON body, such as a proxy's HTML page,
// short enough for a log line.
func Snippet(body string) string {
	body = strings.TrimSpace(body)
	const limit = 200
	if utf8.RuneCountInString(body) <= limit {
		return body
	}
	return string([]rune(body)[:limit]) + "..."
}
