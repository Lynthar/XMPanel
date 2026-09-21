//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// matrixClient is the smallest client that produces a real device: a
// password login. The smoke test uses it to watch a device appear, be
// listed and be revoked by the adapter rather than by the client. Login and
// logout go to loginBase, which is MAS when authentication is delegated.
type matrixClient struct {
	loginBase string
	base      string
	token     string
	DeviceID  string
	UserID    string
	http      *http.Client
}

type matrixError struct {
	Status  int
	Errcode string
	Message string
}

func (e *matrixError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Errcode, e.Message)
}

func matrixLogin(loginBase, base, localpart, password, deviceName string) (*matrixClient, error) {
	c := &matrixClient{loginBase: strings.TrimRight(loginBase, "/"), base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 15 * time.Second}}
	var out struct {
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
		UserID      string `json:"user_id"`
	}
	err := c.doAt(c.loginBase, http.MethodPost, "/_matrix/client/v3/login", map[string]any{
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

// report files an abuse report for an event, which lands in the admin's
// report listing.
func (c *matrixClient) report(roomID, eventID, reason string) error {
	return c.do(http.MethodPost, "/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/report/"+url.PathEscape(eventID), map[string]any{"reason": reason, "score": -100}, nil)
}

// send posts a text message and returns its event id.
func (c *matrixClient) send(roomID, body string) (string, error) {
	var out struct {
		EventID string `json:"event_id"`
	}
	txn := strconv.FormatInt(time.Now().UnixNano(), 36)
	err := c.do(http.MethodPut, "/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/send/m.room.message/"+txn, map[string]any{"msgtype": "m.text", "body": body}, &out)
	return out.EventID, err
}

// upload puts a small file into the media repository and returns its local id.
func (c *matrixClient) upload(name string, data []byte) (string, error) {
	req, err := http.NewRequest(http.MethodPost, c.base+"/_matrix/media/v3/upload?filename="+url.QueryEscape(name), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload: %d %s", resp.StatusCode, body)
	}
	var out struct {
		ContentURI string `json:"content_uri"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	// mxc://server/mediaId
	return out.ContentURI[strings.LastIndex(out.ContentURI, "/")+1:], nil
}

// invitedRooms lists pending invites from one sync, used to see a server
// notice room appear (the server invites the recipient to it).
func (c *matrixClient) invitedRooms() ([]string, error) {
	var out struct {
		Rooms struct {
			Invite map[string]json.RawMessage `json:"invite"`
		} `json:"rooms"`
	}
	if err := c.do(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Rooms.Invite))
	for id := range out.Rooms.Invite {
		ids = append(ids, id)
	}
	return ids, nil
}

func (c *matrixClient) logout() {
	_ = c.doAt(c.loginBase, http.MethodPost, "/_matrix/client/v3/logout", map[string]any{}, nil)
}

func (c *matrixClient) do(method, path string, body, out any) error {
	return c.doAt(c.base, method, path, body, out)
}

func (c *matrixClient) doAt(base, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, base+path, payload)
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
