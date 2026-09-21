//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// matrixClient is the smallest client that produces a real device: a
// password login. The smoke test uses it to watch a device appear, be
// listed and be revoked by the adapter rather than by the client.
type matrixClient struct {
	base     string
	token    string
	DeviceID string
	UserID   string
	http     *http.Client
}

type matrixError struct {
	Status  int
	Errcode string
	Message string
}

func (e *matrixError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Errcode, e.Message)
}

func matrixLogin(base, localpart, password, deviceName string) (*matrixClient, error) {
	c := &matrixClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 15 * time.Second}}
	var out struct {
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
		UserID      string `json:"user_id"`
	}
	err := c.do(http.MethodPost, "/_matrix/client/v3/login", map[string]any{
		"type":                        "m.login.password",
		"identifier":                  map[string]string{"type": "m.id.user", "user": localpart},
		"password":                    password,
		"initial_device_display_name": deviceName,
	}, &out)
	if err != nil {
		return nil, err
	}
	c.token, c.DeviceID, c.UserID = out.AccessToken, out.DeviceID, out.UserID
	return c, nil
}

// whoami reports whether the device's token is still accepted.
func (c *matrixClient) whoami() error {
	return c.do(http.MethodGet, "/_matrix/client/v3/account/whoami", nil, nil)
}

func (c *matrixClient) createRoom(name string, public bool) (string, error) {
	body := map[string]any{"name": name, "preset": "private_chat", "visibility": "private"}
	if public {
		body["preset"], body["visibility"] = "public_chat", "public"
	}
	var out struct {
		RoomID string `json:"room_id"`
	}
	err := c.do(http.MethodPost, "/_matrix/client/v3/createRoom", body, &out)
	return out.RoomID, err
}

func (c *matrixClient) logout() {
	_ = c.do(http.MethodPost, "/_matrix/client/v3/logout", map[string]any{}, nil)
}

func (c *matrixClient) do(method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, payload)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		failure := &matrixError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
		var detail struct {
			Errcode string `json:"errcode"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(data, &detail) == nil && detail.Errcode != "" {
			failure.Errcode, failure.Message = detail.Errcode, detail.Error
		}
		return failure
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
