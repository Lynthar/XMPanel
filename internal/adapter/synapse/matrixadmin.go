package synapse

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/matrixhttp"
)

// The adapter's MatrixAdmin side: Synapse's moderation endpoints, with the
// registration tokens and deactivation rerouted to MAS under delegation.
var _ adapter.MatrixAdmin = (*Adapter)(nil)

// masLifecycle names the MatrixAdmin operations MAS owns under delegation.
var masLifecycle = []adapter.Capability{adapter.CapMatrixDeactivate, adapter.CapMatrixRegTokens}

// declared answers NotSupported before any request for an operation the
// implementation mask or the probe left out of the capability set.
func (a *Adapter) declared(op string, c adapter.Capability) error {
	if !a.Capabilities().Has(c) {
		return adapter.NotSupportedError(op)
	}
	return nil
}

func (a *Adapter) Deactivate(ctx context.Context, id string, erase bool) error {
	const op = "matrix.deactivate"
	viaMAS, mxid, err := a.lifecycleTarget(op, id)
	if err != nil {
		return err
	}
	if viaMAS {
		return a.masAction(ctx, op, mxid, "/deactivate", map[string]bool{"skip_erase": !erase})
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	_, err = a.call(ctx, matrixhttp.Request{
		Op: op, Resource: mxid, Method: http.MethodPost,
		Path: "/_synapse/admin/v1/deactivate/" + url.PathEscape(mxid),
		Body: map[string]bool{"erase": erase},
	}, nil)
	return err
}

func (a *Adapter) SetSuspended(ctx context.Context, id string, suspended bool) error {
	const op = "matrix.suspend"
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
	}
	_, err = a.call(ctx, matrixhttp.Request{
		Op: op, Resource: mxid, Method: http.MethodPut,
		Path: "/_synapse/admin/v1/suspend/" + url.PathEscape(mxid),
		Body: map[string]bool{"suspend": suspended},
	}, nil)
	return err
}

// SetShadowBanned checks the account first: Synapse's servlet does not, and
// answers a missing one with a bare 404 M_UNKNOWN from the store.
func (a *Adapter) SetShadowBanned(ctx context.Context, id string, banned bool) error {
	const op = "matrix.shadow_ban"
	if err := a.declared(op, adapter.CapMatrixShadowBan); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	method := http.MethodPost
	if !banned {
		method = http.MethodDelete
	}
	_, err = a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: method, Path: "/_synapse/admin/v1/users/" + url.PathEscape(mxid) + "/shadow_ban"}, nil)
	return err
}

// Registration tokens live in MAS under delegation (Synapse's servlets are
// not registered then), where a token is addressed by ULID and deletion is
// a revocation that keeps the row.
func (a *Adapter) ListRegistrationTokens(ctx context.Context) ([]adapter.RegistrationToken, error) {
	const op = "matrix.registration_tokens.list"
	if err := a.declared(op, adapter.CapMatrixRegTokens); err != nil {
		return nil, err
	}
	viaMAS, err := a.lifecycle(op)
	if err != nil {
		return nil, err
	}
	if viaMAS {
		tokens, err := a.masRegistrationTokens(ctx, op)
		if err != nil {
			return nil, err
		}
		out := make([]adapter.RegistrationToken, len(tokens))
		for i, tk := range tokens {
			out[i] = tk.token()
		}
		return out, nil
	}
	var page struct {
		Tokens []synapseRegToken `json:"registration_tokens"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_synapse/admin/v1/registration_tokens"}, &page); err != nil {
		return nil, err
	}
	out := make([]adapter.RegistrationToken, len(page.Tokens))
	for i, tk := range page.Tokens {
		out[i] = tk.token()
	}
	return out, nil
}

func (a *Adapter) CreateRegistrationToken(ctx context.Context, req adapter.CreateRegistrationToken) (*adapter.RegistrationToken, error) {
	const op = "matrix.registration_tokens.create"
	if err := a.declared(op, adapter.CapMatrixRegTokens); err != nil {
		return nil, err
	}
	viaMAS, err := a.lifecycle(op)
	if err != nil {
		return nil, err
	}
	if viaMAS {
		body := map[string]any{}
		if req.Token != "" {
			body["token"] = req.Token
		}
		if req.UsesAllowed != nil {
			body["usage_limit"] = *req.UsesAllowed
		}
		if req.ExpiresAt != nil {
			body["expires_at"] = req.ExpiresAt.UTC().Format(time.RFC3339)
		}
		var created struct {
			Data masRegToken `json:"data"`
		}
		if _, err := a.mas.call(ctx, matrixhttp.Request{Op: op, Resource: adapter.MaskToken(req.Token), Method: http.MethodPost, Path: "/api/admin/v1/user-registration-tokens", Body: body}, &created); err != nil {
			return nil, err
		}
		tk := created.Data.token()
		return &tk, nil
	}
	body := map[string]any{}
	if req.Token != "" {
		body["token"] = req.Token
	}
	if req.UsesAllowed != nil {
		body["uses_allowed"] = *req.UsesAllowed
	}
	if req.ExpiresAt != nil {
		body["expiry_time"] = req.ExpiresAt.UnixMilli()
	}
	var created synapseRegToken
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: adapter.MaskToken(req.Token), Method: http.MethodPost, Path: "/_synapse/admin/v1/registration_tokens/new", Body: body}, &created); err != nil {
		return nil, err
	}
	tk := created.token()
	return &tk, nil
}

func (a *Adapter) DeleteRegistrationToken(ctx context.Context, token string) error {
	const op = "matrix.registration_tokens.delete"
	if err := a.declared(op, adapter.CapMatrixRegTokens); err != nil {
		return err
	}
	viaMAS, err := a.lifecycle(op)
	if err != nil {
		return err
	}
	if token == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Err: errors.New("token is required")}
	}
	if viaMAS {
		tokens, err := a.masRegistrationTokens(ctx, op)
		if err != nil {
			return err
		}
		for _, tk := range tokens {
			if tk.Attributes.Token == token {
				if tk.Attributes.RevokedAt != nil {
					return nil
				}
				_, err := a.mas.call(ctx, matrixhttp.Request{Op: op, Resource: adapter.MaskToken(token), Method: http.MethodPost, Path: "/api/admin/v1/user-registration-tokens/" + url.PathEscape(tk.ID) + "/revoke"}, nil)
				return err
			}
		}
		return &adapter.Error{Kind: adapter.NotFound, Op: op, Resource: adapter.MaskToken(token), Status: http.StatusOK, Err: errors.New("registration token not found")}
	}
	_, err = a.call(ctx, matrixhttp.Request{
		Op: op, Resource: adapter.MaskToken(token), Method: http.MethodDelete,
		Path: "/_synapse/admin/v1/registration_tokens/" + url.PathEscape(token), Label: "/_synapse/admin/v1/registration_tokens/{token}",
	}, nil)
	return err
}

func (a *Adapter) ListReports(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.EventReport], error) {
	const op = "matrix.reports"
	if err := a.declared(op, adapter.CapMatrixReports); err != nil {
		return adapter.Page[adapter.EventReport]{}, err
	}
	from, err := offset(op, q.Cursor)
	if err != nil {
		return adapter.Page[adapter.EventReport]{}, err
	}
	query := url.Values{"from": {strconv.Itoa(from)}, "limit": {strconv.Itoa(limitOf(q))}, "dir": {"b"}}
	var page struct {
		Reports []struct {
			ID             int64  `json:"id"`
			ReceivedTS     int64  `json:"received_ts"`
			RoomID         string `json:"room_id"`
			Name           string `json:"name"`
			CanonicalAlias string `json:"canonical_alias"`
			EventID        string `json:"event_id"`
			UserID         string `json:"user_id"`
			Sender         string `json:"sender"`
			Reason         string `json:"reason"`
			Score          int    `json:"score"`
		} `json:"event_reports"`
		NextToken pageToken `json:"next_token"`
		Total     int       `json:"total"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_synapse/admin/v1/event_reports", Query: query}, &page); err != nil {
		return adapter.Page[adapter.EventReport]{}, err
	}
	items := make([]adapter.EventReport, len(page.Reports))
	for i, r := range page.Reports {
		items[i] = adapter.EventReport{
			ID: strconv.FormatInt(r.ID, 10), ReceivedAt: time.UnixMilli(r.ReceivedTS).UTC(),
			RoomID: r.RoomID, RoomName: r.Name, RoomAlias: r.CanonicalAlias, EventID: r.EventID,
			Reporter: r.UserID, Sender: r.Sender, Reason: r.Reason, Score: r.Score,
		}
	}
	total := page.Total
	return adapter.Page[adapter.EventReport]{Items: items, Next: string(page.NextToken), Total: &total}, nil
}

func (a *Adapter) ListAccountMedia(ctx context.Context, id string, q adapter.ListQuery) (adapter.Page[adapter.Media], error) {
	const op = "matrix.media.list"
	mxid, err := a.mxid(op, id)
	if err != nil {
		return adapter.Page[adapter.Media]{}, err
	}
	from, err := offset(op, q.Cursor)
	if err != nil {
		return adapter.Page[adapter.Media]{}, err
	}
	query := url.Values{"from": {strconv.Itoa(from)}, "limit": {strconv.Itoa(limitOf(q))}}
	var page struct {
		Media []struct {
			MediaID            string `json:"media_id"`
			MediaType          string `json:"media_type"`
			MediaLength        int64  `json:"media_length"`
			UploadName         string `json:"upload_name"`
			CreatedTS          int64  `json:"created_ts"`
			LastAccessTS       *int64 `json:"last_access_ts"`
			QuarantinedBy      string `json:"quarantined_by"`
			SafeFromQuarantine bool   `json:"safe_from_quarantine"`
		} `json:"media"`
		NextToken pageToken `json:"next_token"`
		Total     int       `json:"total"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: "/_synapse/admin/v1/users/" + url.PathEscape(mxid) + "/media", Query: query}, &page); err != nil {
		return adapter.Page[adapter.Media]{}, err
	}
	items := make([]adapter.Media, len(page.Media))
	for i, md := range page.Media {
		item := adapter.Media{
			ID: md.MediaID, Type: md.MediaType, Size: md.MediaLength, Name: md.UploadName,
			Quarantined: md.QuarantinedBy != "", Protected: md.SafeFromQuarantine,
		}
		if md.CreatedTS > 0 {
			t := time.UnixMilli(md.CreatedTS).UTC()
			item.CreatedAt = &t
		}
		if md.LastAccessTS != nil && *md.LastAccessTS > 0 {
			t := time.UnixMilli(*md.LastAccessTS).UTC()
			item.LastAccess = &t
		}
		items[i] = item
	}
	total := page.Total
	return adapter.Page[adapter.Media]{Items: items, Next: string(page.NextToken), Total: &total}, nil
}

// QuarantineAccountMedia checks the account first: Synapse answers an
// unknown one with 0 quarantined rather than an error.
func (a *Adapter) QuarantineAccountMedia(ctx context.Context, id string) (int, error) {
	const op = "matrix.media.quarantine"
	if err := a.declared(op, adapter.CapMatrixMediaQuarantine); err != nil {
		return 0, err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return 0, err
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return 0, err
	}
	var out struct {
		NumQuarantined int `json:"num_quarantined"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodPost, Path: "/_synapse/admin/v1/user/" + url.PathEscape(mxid) + "/media/quarantine", Body: map[string]any{}}, &out); err != nil {
		return 0, err
	}
	return out.NumQuarantined, nil
}

// DeleteMedia removes one item of this server's own media; the media id is
// local, so the server name is the configured domain.
func (a *Adapter) DeleteMedia(ctx context.Context, mediaID string) error {
	const op = "matrix.media.delete"
	if mediaID == "" || strings.ContainsAny(mediaID, "/") {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: mediaID, Err: errors.New("media id must be a bare local id")}
	}
	_, err := a.call(ctx, matrixhttp.Request{
		Op: op, Resource: mediaID, Method: http.MethodDelete,
		Path: "/_synapse/admin/v1/media/" + url.PathEscape(a.cfg.Domain) + "/" + url.PathEscape(mediaID),
		Body: map[string]any{},
	}, nil)
	return err
}

// BlockRoom accepts any well-formed room id: Synapse blocks rooms it has
// not seen yet, which is the point of blocking.
func (a *Adapter) BlockRoom(ctx context.Context, roomID string, block bool) error {
	const op = "matrix.room_block"
	if !validRoomID(roomID) {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: roomID, Err: errors.New("room id must be !opaque or !opaque:server")}
	}
	_, err := a.call(ctx, matrixhttp.Request{
		Op: op, Resource: roomID, Method: http.MethodPut,
		Path: "/_synapse/admin/v1/rooms/" + url.PathEscape(roomID) + "/block",
		Body: map[string]bool{"block": block},
	}, nil)
	return err
}

// PurgeRoom is the full-parameter shutdown; like DeleteRoom it checks the
// room exists first, since the v2 endpoint schedules a task for any legal id.
func (a *Adapter) PurgeRoom(ctx context.Context, roomID string, opts adapter.PurgeRoom) (string, error) {
	const op = "matrix.room_purge"
	if _, err := a.roomDetails(ctx, op, roomID); err != nil {
		return "", err
	}
	body := map[string]any{"purge": opts.Purge, "block": opts.Block, "force_purge": opts.ForcePurge}
	if opts.NewRoomUser != "" {
		body["new_room_user_id"] = opts.NewRoomUser
		if opts.NewRoomName != "" {
			body["room_name"] = opts.NewRoomName
		}
		if opts.Message != "" {
			body["message"] = opts.Message
		}
	}
	var out struct {
		DeleteID string `json:"delete_id"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: roomID, Method: http.MethodDelete, Path: "/_synapse/admin/v2/rooms/" + url.PathEscape(roomID), Body: body}, &out); err != nil {
		return "", err
	}
	return out.DeleteID, nil
}

// SendServerNotice needs server_notices configured upstream; Synapse
// answers 400 otherwise, which call turns into NotSupported.
func (a *Adapter) SendServerNotice(ctx context.Context, accountID, body string) error {
	const op = "matrix.server_notice"
	mxid, err := a.mxid(op, accountID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: mxid, Err: errors.New("notice body is required")}
	}
	_, err = a.call(ctx, matrixhttp.Request{
		Op: op, Resource: mxid, Method: http.MethodPost, Path: "/_synapse/admin/v1/send_server_notice",
		Body: map[string]any{"user_id": mxid, "content": map[string]string{"msgtype": "m.text", "body": body}},
	}, nil)
	return err
}

func (a *Adapter) ListFederationDestinations(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.FederationDestination], error) {
	const op = "matrix.federation"
	from, err := offset(op, q.Cursor)
	if err != nil {
		return adapter.Page[adapter.FederationDestination]{}, err
	}
	query := url.Values{"from": {strconv.Itoa(from)}, "limit": {strconv.Itoa(limitOf(q))}}
	var page struct {
		Destinations []struct {
			Destination   string `json:"destination"`
			RetryLastTS   int64  `json:"retry_last_ts"`
			RetryInterval int64  `json:"retry_interval"`
			FailureTS     *int64 `json:"failure_ts"`
		} `json:"destinations"`
		NextToken pageToken `json:"next_token"`
		Total     int       `json:"total"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_synapse/admin/v1/federation/destinations", Query: query}, &page); err != nil {
		return adapter.Page[adapter.FederationDestination]{}, err
	}
	items := make([]adapter.FederationDestination, len(page.Destinations))
	for i, d := range page.Destinations {
		item := adapter.FederationDestination{Destination: d.Destination, RetryIntervalMS: d.RetryInterval, Healthy: d.RetryLastTS == 0 && d.FailureTS == nil}
		if d.RetryLastTS > 0 {
			t := time.UnixMilli(d.RetryLastTS).UTC()
			item.LastFailureAt = &t
		}
		if d.FailureTS != nil && *d.FailureTS > 0 {
			t := time.UnixMilli(*d.FailureTS).UTC()
			item.FailingSince = &t
		}
		items[i] = item
	}
	total := page.Total
	return adapter.Page[adapter.FederationDestination]{Items: items, Next: string(page.NextToken), Total: &total}, nil
}

// masRegistrationTokens lists every MAS token, following the pagination links.
func (a *Adapter) masRegistrationTokens(ctx context.Context, op string) ([]masRegToken, error) {
	var all []masRegToken
	path := "/api/admin/v1/user-registration-tokens?page[first]=100"
	for path != "" {
		var page struct {
			Data  []masRegToken `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if _, err := a.mas.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: path}, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		path = page.Links.Next
	}
	return all, nil
}

type synapseRegToken struct {
	Token       string `json:"token"`
	UsesAllowed *int   `json:"uses_allowed"`
	Pending     int    `json:"pending"`
	Completed   int    `json:"completed"`
	ExpiryTime  *int64 `json:"expiry_time"`
}

// token derives Valid the way Synapse's registration flow does: not expired
// and, with a use limit, uses left after the pending ones. Synapse treats a
// limit of 0 as no limit, so it is reported as unlimited.
func (t synapseRegToken) token() adapter.RegistrationToken {
	out := adapter.RegistrationToken{Token: t.Token, UsesAllowed: t.UsesAllowed, Used: t.Completed, Pending: t.Pending, Valid: true}
	if t.UsesAllowed != nil && *t.UsesAllowed == 0 {
		out.UsesAllowed = nil
	}
	if t.ExpiryTime != nil {
		expires := time.UnixMilli(*t.ExpiryTime).UTC()
		out.ExpiresAt = &expires
		if time.Now().After(expires) {
			out.Valid = false
		}
	}
	if out.UsesAllowed != nil && t.Pending+t.Completed >= *out.UsesAllowed {
		out.Valid = false
	}
	return out
}

type masRegToken struct {
	ID         string `json:"id"`
	Attributes struct {
		Token      string  `json:"token"`
		Valid      bool    `json:"valid"`
		UsageLimit *int    `json:"usage_limit"`
		TimesUsed  int     `json:"times_used"`
		CreatedAt  string  `json:"created_at"`
		ExpiresAt  *string `json:"expires_at"`
		RevokedAt  *string `json:"revoked_at"`
	} `json:"attributes"`
}

func (t masRegToken) token() adapter.RegistrationToken {
	at := t.Attributes
	out := adapter.RegistrationToken{Token: at.Token, UsesAllowed: at.UsageLimit, Used: at.TimesUsed, Valid: at.Valid, Revoked: at.RevokedAt != nil}
	if created, err := time.Parse(time.RFC3339Nano, at.CreatedAt); err == nil {
		out.CreatedAt = &created
	}
	if at.ExpiresAt != nil {
		if expires, err := time.Parse(time.RFC3339Nano, *at.ExpiresAt); err == nil {
			out.ExpiresAt = &expires
		}
	}
	return out
}
