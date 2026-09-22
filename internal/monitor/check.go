package monitor

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Check kinds. These strings reach the database and the frontend's locale
// keys, so they are as permanent as the rows that carry them.
const (
	KindTLSCert    = "tls_cert"
	KindDNSSRV     = "dns_srv"
	KindWellKnown  = "well_known"
	KindFederation = "federation"
)

const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// certWarnBefore is how long before expiry a certificate starts to warn. Two
// weeks covers a missed ACME renewal plus a weekend.
const certWarnBefore = 14 * 24 * time.Hour

// keyWarnBefore is how long before a Matrix signing key's valid_until_ts the
// federation check warns. Servers refresh these continuously; one that has
// stopped is about to drop out of federation silently.
const keyWarnBefore = time.Hour

// CheckDetail is the stored result of one check. Every item carries its own
// verdict so the frontend renders one list without knowing the kind, and the
// facts stay as values rather than sentences so both locales can phrase them.
type CheckDetail struct {
	Items []CheckItem `json:"items"`
}

// CheckItem is one thing that was checked: a TCP endpoint, a DNS name or a URL.
type CheckItem struct {
	Target   string     `json:"target"`
	Status   string     `json:"status"`
	Values   []string   `json:"values,omitempty"`
	NotAfter *time.Time `json:"not_after,omitempty"`
	Error    string     `json:"error,omitempty"`
}

// worst folds the items' verdicts into the check's own.
func (d CheckDetail) worst() string {
	status := StatusOK
	for _, item := range d.Items {
		switch item.Status {
		case StatusFail:
			return StatusFail
		case StatusWarn:
			status = StatusWarn
		}
	}
	return status
}

func checkResult(kind string, items ...CheckItem) Check {
	detail := CheckDetail{Items: items}
	return Check{Kind: kind, TS: time.Now().UTC(), Status: detail.worst(), Detail: detail}
}

// failedItem records a check that could not be carried out. The error text is
// the upstream's own; it is shown verbatim next to the translated label.
func failedItem(target string, err error) CheckItem {
	return CheckItem{Target: target, Status: StatusFail, Error: err.Error()}
}

// checkServer runs every check that applies to a server. Each check gets its
// own timeout, so one unresponsive target does not eat the others' budget.
func (m *Monitor) checkServer(ctx context.Context, s serverRow) []Check {
	if s.protocol == adapter.ProtocolMatrix {
		return []Check{
			m.timed(ctx, func(ctx context.Context) Check { return matrixDNSCheck(ctx, s.domain) }),
			m.timed(ctx, func(ctx context.Context) Check { return matrixWellKnownCheck(ctx, s.domain) }),
			m.timed(ctx, func(ctx context.Context) Check { return matrixTLSCheck(ctx, s.domain) }),
			m.timed(ctx, func(ctx context.Context) Check { return matrixFederationCheck(ctx, s.domain) }),
		}
	}
	checks := []Check{
		m.timed(ctx, func(ctx context.Context) Check { return xmppDNSCheck(ctx, s.domain) }),
		m.timed(ctx, func(ctx context.Context) Check { return xmppWellKnownCheck(ctx, s.domain) }),
		m.timed(ctx, func(ctx context.Context) Check { return xmppTLSCheck(ctx, s.domain) }),
	}
	if federation := m.xmppFederationCheck(ctx, s.id); federation != nil {
		checks = append(checks, *federation)
	}
	return checks
}

func (m *Monitor) timed(ctx context.Context, fn func(context.Context) Check) Check {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return fn(ctx)
}

// srvTarget is one resolved SRV record.
type srvTarget struct {
	host string
	port uint16
}

func (t srvTarget) String() string { return net.JoinHostPort(t.host, strconv.Itoa(int(t.port))) }

// lookupSRV resolves one SRV name. A single "." target is the explicit "no
// service here" record of RFC 2782 and is reported as such, not as an address.
//
// The lookup runs in its own goroutine because the platform resolver does not
// honour the context for SRV records on every platform — on Windows an
// unanswered query costs about 22 seconds whatever the deadline says, which
// would stall the scheduler far past the per-probe budget. The abandoned
// goroutine ends when the resolver gives up.
func lookupSRV(ctx context.Context, service, domain string) (targets []srvTarget, disabled bool, err error) {
	type answer struct {
		records []*net.SRV
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		_, records, err := net.DefaultResolver.LookupSRV(ctx, service, "tcp", domain)
		done <- answer{records: records, err: err}
	}()
	var resolved answer
	select {
	case resolved = <-done:
	case <-ctx.Done():
		return nil, false, fmt.Errorf("DNS lookup did not answer: %w", ctx.Err())
	}
	records, err := resolved.records, resolved.err
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, record := range records {
		host := strings.TrimSuffix(record.Target, ".")
		if host == "" {
			return nil, true, nil
		}
		targets = append(targets, srvTarget{host: host, port: record.Port})
	}
	return targets, false, nil
}

// srvItem turns one SRV lookup into a reportable item. A missing record is a
// warning rather than a failure: SRV is optional for both protocols, and the
// default port still works.
func srvItem(ctx context.Context, service, domain string) CheckItem {
	name := "_" + service + "._tcp." + domain
	targets, disabled, err := lookupSRV(ctx, service, domain)
	switch {
	case err != nil:
		return failedItem(name, err)
	case disabled:
		return CheckItem{Target: name, Status: StatusOK, Values: []string{"."}}
	case len(targets) == 0:
		return CheckItem{Target: name, Status: StatusWarn}
	}
	values := make([]string, 0, len(targets))
	for _, target := range targets {
		values = append(values, target.String())
	}
	return CheckItem{Target: name, Status: StatusOK, Values: values}
}

func xmppDNSCheck(ctx context.Context, domain string) Check {
	return checkResult(KindDNSSRV,
		srvItem(ctx, "xmpp-client", domain),
		srvItem(ctx, "xmpp-server", domain),
		srvItem(ctx, "xmpps-client", domain),
		srvItem(ctx, "xmpps-server", domain),
	)
}

func matrixDNSCheck(ctx context.Context, domain string) Check {
	return checkResult(KindDNSSRV,
		srvItem(ctx, "matrix-fed", domain),
		srvItem(ctx, "matrix", domain),
	)
}

// httpClient is shared by the HTTP-shaped checks. Redirects are followed
// because well-known delegation commonly lands on a CDN, and the per-check
// context bounds the whole chain.
var httpClient = &http.Client{Timeout: probeTimeout}

// fetch retrieves a URL and returns its body, capped so a misconfigured host
// serving its homepage cannot fill memory.
func fetch(ctx context.Context, url string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, body, err
}

// matrixDelegation reports where federation traffic for a server name goes and
// which name its certificate must carry. Under well-known delegation that is
// the delegated host; under SRV it stays the original server name, which is
// the distinction operators get wrong.
func matrixDelegation(ctx context.Context, domain string) (target srvTarget, certName string) {
	if host, ok := matrixWellKnownServer(ctx, domain); ok {
		name, port := splitHostPort(host, 8448)
		return srvTarget{host: name, port: port}, name
	}
	for _, service := range []string{"matrix-fed", "matrix"} {
		targets, _, err := lookupSRV(ctx, service, domain)
		if err == nil && len(targets) > 0 {
			return targets[0], domain
		}
	}
	return srvTarget{host: domain, port: 8448}, domain
}

// matrixWellKnownServer reads the m.server delegation, if any.
func matrixWellKnownServer(ctx context.Context, domain string) (string, bool) {
	status, body, err := fetch(ctx, "https://"+domain+"/.well-known/matrix/server")
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var doc struct {
		Server string `json:"m.server"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Server == "" {
		return "", false
	}
	return doc.Server, true
}

func splitHostPort(value string, defaultPort uint16) (string, uint16) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return value, defaultPort
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return host, defaultPort
	}
	return host, uint16(port)
}

func matrixWellKnownCheck(ctx context.Context, domain string) Check {
	server := wellKnownItem(ctx, "https://"+domain+"/.well-known/matrix/server", func(body []byte) (string, error) {
		var doc struct {
			Server string `json:"m.server"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return "", errNotJSON
		}
		if doc.Server == "" {
			return "", errors.New("no m.server in response")
		}
		return doc.Server, nil
	})
	client := wellKnownItem(ctx, "https://"+domain+"/.well-known/matrix/client", func(body []byte) (string, error) {
		var doc struct {
			Homeserver struct {
				BaseURL string `json:"base_url"`
			} `json:"m.homeserver"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return "", errNotJSON
		}
		if doc.Homeserver.BaseURL == "" {
			return "", errors.New("no m.homeserver.base_url in response")
		}
		return doc.Homeserver.BaseURL, nil
	})
	return checkResult(KindWellKnown, server, client)
}

var errNotJSON = errors.New("response is not JSON")

// wellKnownItem reads one Matrix well-known document and reports what it
// delegates to. A 404 is a warning, not a failure: delegation is optional, and
// its absence only means clients and servers must reach the domain itself.
func wellKnownItem(ctx context.Context, url string, parse func([]byte) (string, error)) CheckItem {
	status, body, err := fetch(ctx, url)
	if err != nil {
		return failedItem(url, err)
	}
	if status == http.StatusNotFound {
		return CheckItem{Target: url, Status: StatusWarn}
	}
	if status != http.StatusOK {
		return CheckItem{Target: url, Status: StatusFail, Error: "HTTP " + strconv.Itoa(status)}
	}
	value, err := parse(body)
	if err != nil {
		return failedItem(url, err)
	}
	return CheckItem{Target: url, Status: StatusOK, Values: []string{value}}
}

// xmppWellKnownCheck reads the XEP-0156 alternative connection methods. Both
// the XRD and the JSON form are published in practice; either one answering is
// enough.
func xmppWellKnownCheck(ctx context.Context, domain string) Check {
	target := "https://" + domain + "/.well-known/host-meta"
	status, body, err := fetch(ctx, target)
	if err != nil {
		return checkResult(KindWellKnown, failedItem(target, err))
	}
	if status == http.StatusOK {
		return checkResult(KindWellKnown, altConnectionsItem(target, body, parseHostMetaXRD))
	}

	jsonTarget := target + ".json"
	jsonStatus, jsonBody, err := fetch(ctx, jsonTarget)
	switch {
	case err != nil:
		return checkResult(KindWellKnown, failedItem(jsonTarget, err))
	case jsonStatus == http.StatusOK:
		return checkResult(KindWellKnown, altConnectionsItem(jsonTarget, jsonBody, parseHostMetaJSON))
	case status == http.StatusNotFound && jsonStatus == http.StatusNotFound:
		// Alternative connection methods are optional; a server reachable on
		// 5222 works without them.
		return checkResult(KindWellKnown, CheckItem{Target: target, Status: StatusWarn})
	}
	return checkResult(KindWellKnown,
		CheckItem{Target: target, Status: StatusFail, Error: "HTTP " + strconv.Itoa(status)})
}

const altConnectionPrefix = "urn:xmpp:alt-connections:"

// altConnectionsItem reports which alternative connection methods a host-meta
// document advertises. A document that parses but advertises none is a
// warning: it was published for nothing.
func altConnectionsItem(target string, body []byte, parse func([]byte) ([]string, error)) CheckItem {
	rels, err := parse(body)
	if err != nil {
		return failedItem(target, err)
	}
	values := make([]string, 0, len(rels))
	for _, rel := range rels {
		if method, ok := strings.CutPrefix(rel, altConnectionPrefix); ok {
			values = append(values, method)
		}
	}
	if len(values) == 0 {
		return CheckItem{Target: target, Status: StatusWarn}
	}
	return CheckItem{Target: target, Status: StatusOK, Values: values}
}

func parseHostMetaXRD(body []byte) ([]string, error) {
	var doc struct {
		Links []struct {
			Rel string `xml:"rel,attr"`
		} `xml:"Link"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("response is not XRD")
	}
	rels := make([]string, 0, len(doc.Links))
	for _, link := range doc.Links {
		rels = append(rels, link.Rel)
	}
	return rels, nil
}

func parseHostMetaJSON(body []byte) ([]string, error) {
	var doc struct {
		Links []struct {
			Rel string `json:"rel"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errNotJSON
	}
	rels := make([]string, 0, len(doc.Links))
	for _, link := range doc.Links {
		rels = append(rels, link.Rel)
	}
	return rels, nil
}

// matrixFederationCheck reaches the federation port the rest of the network
// would use and reads the server's signing keys. An expiring valid_until_ts is
// a common cause of federation dropping out with no other symptom.
func matrixFederationCheck(ctx context.Context, domain string) Check {
	target, _ := matrixDelegation(ctx, domain)
	base := "https://" + target.String()

	version := CheckItem{Target: base + "/_matrix/federation/v1/version", Status: StatusOK}
	status, body, err := fetch(ctx, version.Target)
	switch {
	case err != nil:
		version = failedItem(version.Target, err)
	case status != http.StatusOK:
		version.Status, version.Error = StatusFail, "HTTP "+strconv.Itoa(status)
	default:
		var doc struct {
			Server struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"server"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			version.Status, version.Error = StatusFail, "response is not JSON"
		} else {
			version.Values = []string{strings.TrimSpace(doc.Server.Name + " " + doc.Server.Version)}
		}
	}
	return checkResult(KindFederation, version, signingKeyItem(ctx, base, domain))
}

// signingKeyItem reads /_matrix/key/v2/server and reports how long the
// published keys stay valid.
func signingKeyItem(ctx context.Context, base, domain string) CheckItem {
	item := CheckItem{Target: base + "/_matrix/key/v2/server", Status: StatusOK}
	status, body, err := fetch(ctx, item.Target)
	if err != nil {
		return failedItem(item.Target, err)
	}
	if status != http.StatusOK {
		item.Status, item.Error = StatusFail, "HTTP "+strconv.Itoa(status)
		return item
	}
	var doc struct {
		ServerName    string `json:"server_name"`
		ValidUntilTS  int64  `json:"valid_until_ts"`
		VerifyKeysRaw map[string]struct {
			Key string `json:"key"`
		} `json:"verify_keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		item.Status, item.Error = StatusFail, "response is not JSON"
		return item
	}
	if doc.ServerName != domain {
		item.Status = StatusWarn
		item.Error = "keys are signed for " + doc.ServerName
	}
	if doc.ValidUntilTS > 0 {
		validUntil := time.UnixMilli(doc.ValidUntilTS).UTC()
		item.NotAfter = &validUntil
		if time.Until(validUntil) < keyWarnBefore && item.Status == StatusOK {
			item.Status = StatusWarn
		}
	}
	item.Values = append(item.Values, strconv.Itoa(len(doc.VerifyKeysRaw)))
	return item
}

// xmppFederationCheck reports the server-to-server connection count, which
// only ejabberd exposes. Prosody reports no number, and a check row claiming
// nothing is worse than no row, so nil means "do not record".
func (m *Monitor) xmppFederationCheck(ctx context.Context, serverID int64) *Check {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	a, _, err := m.adapters.Get(ctx, serverID)
	if err != nil {
		return nil
	}
	stats, err := a.Stats(ctx)
	if err != nil || stats.S2SConnections == nil {
		return nil
	}
	result := checkResult(KindFederation, CheckItem{
		Target: "s2s",
		Status: StatusOK,
		Values: []string{strconv.Itoa(*stats.S2SConnections)},
	})
	return &result
}

func expiryStatus(notAfter time.Time) string {
	switch {
	case time.Now().After(notAfter):
		return StatusFail
	case time.Until(notAfter) < certWarnBefore:
		return StatusWarn
	default:
		return StatusOK
	}
}
