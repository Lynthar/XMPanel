package synapse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// masClient talks to Matrix Authentication Service's admin API with a token
// from the client_credentials grant, which it fetches itself and renews
// before expiry because that grant comes with no refresh token.
type masClient struct {
	base       string
	clientID   string
	secret     string
	httpClient *http.Client
	mu         sync.Mutex
	token      string
	expiresAt  time.Time
	inflight   chan struct{} // closed when the fetch in progress ends
	lastErr    error
}

const masScope = "urn:mas:admin"

func newMASClient(creds *adapter.MASCredentials, httpClient *http.Client) *masClient {
	return &masClient{
		base:       strings.TrimRight(creds.Endpoint, "/"),
		clientID:   creds.ClientID,
		secret:     creds.ClientSecret,
		httpClient: httpClient,
	}
}

// masUser is the JSON:API resource the admin API returns for a user; the id
// is a ULID, not an MXID, and every mutation is addressed by it.
type masUser struct {
	ID         string `json:"id"`
	Attributes struct {
		Username      string  `json:"username"`
		CreatedAt     string  `json:"created_at"`
		LockedAt      *string `json:"locked_at"`
		DeactivatedAt *string `json:"deactivated_at"`
		Admin         bool    `json:"admin"`
	} `json:"attributes"`
}

func masUserPath(ulid string) string {
	return "/api/admin/v1/users/" + url.PathEscape(ulid)
}

// call performs one admin API request. A 401 on a cached token means MAS
// revoked it early, so the token is dropped and the request sent once more.
func (m *masClient) call(ctx context.Context, req request, out any) (int, error) {
	for attempt := 0; ; attempt++ {
		token, fresh, err := m.accessToken(ctx, req.op)
		if err != nil {
			return 0, err
		}
		status, err := m.send(ctx, req, token, out)
		if status == http.StatusUnauthorized && !fresh && attempt == 0 {
			m.forget(token)
			continue
		}
		return status, err
	}
}

func (m *masClient) send(ctx context.Context, req request, token string, out any) (status int, err error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: req.op, Resource: req.resource}
	defer func() {
		if err != nil {
			failure.Err = err
			err = failure
		}
	}()
	var payload []byte
	if req.body != nil {
		if payload, err = json.Marshal(req.body); err != nil {
			return 0, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}
	header := http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}}
	if req.body != nil {
		header.Set("Content-Type", "application/json")
	}
	target := m.base + req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	status, respBody, err := transport(ctx, m.httpClient, req.method, target, header, payload)
	if err != nil {
		if status == 0 {
			failure.Kind = adapter.Unreachable
			return 0, fmt.Errorf("failed to connect to MAS: %w", err)
		}
		failure.Status = status
		return status, fmt.Errorf("failed to read MAS response: %w", err)
	}
	failure.Status = status
	if status >= 200 && status < 300 {
		if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return status, fmt.Errorf("unexpected MAS response body: %w", err)
			}
		}
		return status, nil
	}
	failure.Kind = classifyMAS(status)
	return status, fmt.Errorf("MAS %s %s: %s", req.method, req.shown(), masMessage(respBody, status))
}

// accessToken returns the cached token, or fetches one and reports it as
// fresh. One goroutine fetches while the others wait for its outcome, each
// honouring its own context; the mutex is never held across the exchange.
func (m *masClient) accessToken(ctx context.Context, op string) (token string, fresh bool, err error) {
	for {
		m.mu.Lock()
		if m.token != "" && time.Now().Before(m.expiresAt) {
			token = m.token
			m.mu.Unlock()
			return token, false, nil
		}
		if done := m.inflight; done != nil {
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return "", false, &adapter.Error{Kind: adapter.Unreachable, Op: op, Resource: m.base + "/oauth2/token", Err: ctx.Err()}
			}
			m.mu.Lock()
			token, err = m.token, m.lastErr
			m.mu.Unlock()
			// A fetch that died with its caller's context says nothing about
			// MAS; the next caller in line fetches for itself.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if err != nil {
				return "", false, err
			}
			return token, true, nil
		}
		done := make(chan struct{})
		m.inflight = done
		m.mu.Unlock()

		token, expiresAt, err := m.fetch(ctx, op)
		m.mu.Lock()
		if err == nil {
			m.token, m.expiresAt = token, expiresAt
		} else {
			m.token = ""
		}
		m.lastErr = err
		m.inflight = nil
		close(done)
		m.mu.Unlock()
		if err != nil {
			return "", false, err
		}
		return token, true, nil
	}
}

// fetch performs the client_credentials grant. Renewal is scheduled a minute
// early, or halfway through a short lifetime.
func (m *masClient) fetch(ctx context.Context, op string) (string, time.Time, error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: op, Resource: m.base + "/oauth2/token"}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {masScope}}
	header := http.Header{
		"Content-Type":  {"application/x-www-form-urlencoded"},
		"Accept":        {"application/json"},
		"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(m.clientID+":"+m.secret))},
	}
	status, respBody, err := transport(ctx, m.httpClient, http.MethodPost, m.base+"/oauth2/token", header, []byte(form.Encode()))
	if err != nil {
		if status == 0 {
			failure.Kind = adapter.Unreachable
		}
		failure.Status = status
		failure.Err = fmt.Errorf("failed to reach MAS token endpoint: %w", err)
		return "", time.Time{}, failure
	}
	failure.Status = status
	if status != http.StatusOK {
		failure.Kind = classifyMAS(status)
		// invalid_client arrives as 400 or 401 depending on the auth method.
		if status == http.StatusBadRequest && oauthErrorCode(respBody) == "invalid_client" {
			failure.Kind = adapter.Unauthorized
		}
		failure.Err = fmt.Errorf("MAS token request: %s", masMessage(respBody, status))
		return "", time.Time{}, failure
	}
	var grant struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &grant); err != nil || grant.AccessToken == "" {
		failure.Err = errors.New("MAS token response carried no access_token")
		return "", time.Time{}, failure
	}
	ttl := time.Duration(grant.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	margin := time.Minute
	if ttl <= 2*margin {
		margin = ttl / 2
	}
	return grant.AccessToken, time.Now().Add(ttl - margin), nil
}

func (m *masClient) forget(token string) {
	m.mu.Lock()
	if m.token == token {
		m.token = ""
	}
	m.mu.Unlock()
}

func classifyMAS(status int) adapter.Kind {
	switch status {
	case http.StatusUnauthorized:
		return adapter.Unauthorized
	case http.StatusForbidden:
		return adapter.Forbidden
	case http.StatusNotFound:
		return adapter.NotFound
	case http.StatusConflict:
		return adapter.Conflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return adapter.Invalid
	case http.StatusTooManyRequests:
		return adapter.RateLimited
	}
	return adapter.Upstream
}

// masMessage reads the admin API's {errors:[{title}]} body or the token
// endpoint's {error, error_description} body, else keeps a snippet.
func masMessage(body []byte, status int) string {
	var api struct {
		Errors []struct {
			Title string `json:"title"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &api) == nil && len(api.Errors) > 0 && api.Errors[0].Title != "" {
		return api.Errors[0].Title
	}
	var oauth struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &oauth) == nil && oauth.Error != "" {
		if oauth.Description != "" {
			return oauth.Error + ": " + oauth.Description
		}
		return oauth.Error
	}
	if message := snippet(string(body)); message != "" {
		return message
	}
	return http.StatusText(status)
}

func oauthErrorCode(body []byte) string {
	var oauth struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &oauth)
	return oauth.Error
}

// transport performs one HTTP exchange and returns the status and body. A
// status of 0 with an error means the server was not reached; the cause is
// kept so a cancelled context stays recognisable through errors.Is.
func transport(ctx context.Context, client *http.Client, method, target string, header http.Header, body []byte) (int, []byte, error) {
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
