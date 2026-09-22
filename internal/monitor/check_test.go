package monitor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckDetailWorst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []string
		want  string
	}{
		{"empty", nil, StatusOK},
		{"all ok", []string{StatusOK, StatusOK}, StatusOK},
		{"one warn", []string{StatusOK, StatusWarn}, StatusWarn},
		{"warn loses to fail", []string{StatusWarn, StatusFail}, StatusFail},
		{"fail first", []string{StatusFail, StatusOK}, StatusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var detail CheckDetail
			for _, status := range tc.items {
				detail.Items = append(detail.Items, CheckItem{Status: status})
			}
			if got := detail.worst(); got != tc.want {
				t.Errorf("worst = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpiryStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want string
	}{
		{"expired", -time.Hour, StatusFail},
		{"expires within the warning window", 13 * 24 * time.Hour, StatusWarn},
		{"plenty of time", 90 * 24 * time.Hour, StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := expiryStatus(time.Now().Add(tc.in)); got != tc.want {
				t.Errorf("expiryStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// A missing well-known document is a warning because delegation is optional;
// a document that is served but unusable is a failure.
func TestWellKnownItem(t *testing.T) {
	parse := func(body []byte) (string, error) {
		var doc struct {
			Server string `json:"m.server"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return "", errNotJSON
		}
		if doc.Server == "" {
			return "", errNotJSON
		}
		return doc.Server, nil
	}
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus string
		wantValue  string
	}{
		{"delegated", 200, `{"m.server":"matrix.example.com:8448"}`, StatusOK, "matrix.example.com:8448"},
		{"not published", 404, "", StatusWarn, ""},
		{"server error", 500, "", StatusFail, ""},
		{"not json", 200, "<html>", StatusFail, ""},
		{"field missing", 200, `{}`, StatusFail, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			item := wellKnownItem(context.Background(), upstream.URL, parse)
			if item.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", item.Status, tc.wantStatus)
			}
			if tc.wantValue != "" && (len(item.Values) != 1 || item.Values[0] != tc.wantValue) {
				t.Errorf("values = %v, want %q", item.Values, tc.wantValue)
			}
		})
	}
}

func TestAltConnectionsItem(t *testing.T) {
	const xrd = `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
		<Link rel="urn:xmpp:alt-connections:websocket" href="wss://example.com/ws"/>
		<Link rel="lrdd" href="https://example.com/lrdd"/>
	</XRD>`
	item := altConnectionsItem("host-meta", []byte(xrd), parseHostMetaXRD)
	if item.Status != StatusOK || len(item.Values) != 1 || item.Values[0] != "websocket" {
		t.Errorf("XRD item = %+v, want the websocket method alone", item)
	}

	const jsonDoc = `{"links":[{"rel":"urn:xmpp:alt-connections:xbosh","href":"https://example.com/bosh"}]}`
	item = altConnectionsItem("host-meta.json", []byte(jsonDoc), parseHostMetaJSON)
	if item.Status != StatusOK || len(item.Values) != 1 || item.Values[0] != "xbosh" {
		t.Errorf("JSON item = %+v, want the xbosh method", item)
	}

	// A document published with no alternative connection method at all was
	// published for nothing, which is worth saying.
	item = altConnectionsItem("host-meta.json", []byte(`{"links":[]}`), parseHostMetaJSON)
	if item.Status != StatusWarn {
		t.Errorf("empty document status = %q, want %q", item.Status, StatusWarn)
	}

	item = altConnectionsItem("host-meta.json", []byte(`not json`), parseHostMetaJSON)
	if item.Status != StatusFail {
		t.Errorf("malformed document status = %q, want %q", item.Status, StatusFail)
	}
}

// The signing keys carry the date federation silently breaks on, so the check
// reports it and warns before it arrives.
func TestSigningKeyItem(t *testing.T) {
	const domain = "example.com"
	for _, tc := range []struct {
		name         string
		serverName   string
		validUntil   time.Duration
		wantStatus   string
		wantNotAfter bool
	}{
		{"healthy", domain, 24 * time.Hour, StatusOK, true},
		{"about to expire", domain, 30 * time.Minute, StatusWarn, true},
		{"already expired", domain, -time.Minute, StatusWarn, true},
		{"signed for another server", "other.example", 24 * time.Hour, StatusWarn, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"server_name":    tc.serverName,
					"valid_until_ts": time.Now().Add(tc.validUntil).UnixMilli(),
					"verify_keys":    map[string]any{"ed25519:a": map[string]string{"key": "abc"}},
				})
			}))
			defer upstream.Close()

			item := signingKeyItem(context.Background(), upstream.URL, domain)
			if item.Status != tc.wantStatus {
				t.Errorf("status = %q (%s), want %q", item.Status, item.Error, tc.wantStatus)
			}
			if tc.wantNotAfter && item.NotAfter == nil {
				t.Error("no valid_until reported")
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer upstream.Close()
		if item := signingKeyItem(context.Background(), upstream.URL, domain); item.Status != StatusFail {
			t.Errorf("status = %q, want %q", item.Status, StatusFail)
		}
	})
}

// An expired certificate must still be read: an operator needs the date, and a
// handshake that refused it would report nothing at all.
func TestCertificateAtReadsExpiredCertificate(t *testing.T) {
	notAfter := time.Now().Add(-time.Hour)
	listener := tlsListener(t, notAfter)

	item := certificateAt(context.Background(), listener, "example.com", nil)

	if item.Status != StatusFail {
		t.Errorf("status = %q, want %q", item.Status, StatusFail)
	}
	if item.NotAfter == nil || item.NotAfter.Unix() != notAfter.Unix() {
		t.Errorf("not_after = %v, want the certificate's %v", item.NotAfter, notAfter)
	}
}

// A self-signed certificate that has not expired still fails the chain check,
// and the reason travels with the item.
func TestCertificateAtReportsUntrustedChain(t *testing.T) {
	listener := tlsListener(t, time.Now().Add(90*24*time.Hour))

	item := certificateAt(context.Background(), listener, "example.com", nil)

	if item.Status != StatusFail || item.Error == "" {
		t.Fatalf("item = %+v, want a failure carrying the chain error", item)
	}
	if item.NotAfter == nil {
		t.Error("an untrusted chain still has an expiry worth reporting")
	}
}

// The STARTTLS upgrade has to complete before the handshake; a server that
// never offers it fails the check instead of hanging.
func TestCertificateAtOverSTARTTLS(t *testing.T) {
	t.Run("upgrades", func(t *testing.T) {
		address := xmppListener(t, true)
		item := certificateAt(context.Background(), address, "example.com", startTLS(streamClient))
		if item.NotAfter == nil {
			t.Fatalf("item = %+v, want a certificate read through STARTTLS", item)
		}
	})

	t.Run("not offered", func(t *testing.T) {
		address := xmppListener(t, false)
		item := certificateAt(context.Background(), address, "example.com", startTLS(streamClient))
		if item.Status != StatusFail || !strings.Contains(item.Error, "STARTTLS") {
			t.Fatalf("item = %+v, want a STARTTLS failure", item)
		}
	})
}

// The platform resolver does not honour the context for SRV records on every
// platform, so lookupSRV enforces the deadline itself. Without that the
// per-probe budget is fiction and one dead domain stalls a whole round.
func TestLookupSRVHonoursTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := lookupSRV(ctx, "xmpp-client", "srv-check.invalid"); err == nil {
			t.Error("a cancelled lookup reported success")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lookupSRV ignored the cancelled context")
	}
}

func TestXMLAttrEscapesTheStreamHeader(t *testing.T) {
	if got := xmlAttr("a'b&c<d"); got != "a&apos;b&amp;c&lt;d" {
		t.Errorf("xmlAttr = %q", got)
	}
}

// selfSigned builds a certificate for example.com expiring at notAfter.
func selfSigned(t *testing.T, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsListener serves the certificate on a port that speaks TLS immediately.
func tlsListener(t *testing.T, notAfter time.Time) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t, notAfter)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, conn) }()
		}
	}()
	return listener.Addr().String()
}

// xmppListener answers a stream header with features that either offer
// STARTTLS and upgrade, or offer nothing at all.
func xmppListener(t *testing.T, offerTLS bool) string {
	t.Helper()
	certificate := selfSigned(t, time.Now().Add(90*24*time.Hour))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveXMPP(conn, certificate, offerTLS)
		}
	}()
	return listener.Addr().String()
}

func serveXMPP(conn net.Conn, certificate tls.Certificate, offerTLS bool) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 1024)
	if _, err := conn.Read(buf); err != nil {
		return
	}
	features := `<stream:features/>`
	if offerTLS {
		features = `<stream:features><starttls xmlns='urn:ietf:params:xml:ns:xmpp-tls'/></stream:features>`
	}
	header := `<?xml version='1.0'?><stream:stream xmlns='jabber:client' xmlns:stream='http://etherx.jabber.org/streams'>`
	if _, err := io.WriteString(conn, header+features); err != nil {
		return
	}
	if !offerTLS {
		return
	}
	if _, err := conn.Read(buf); err != nil {
		return
	}
	if _, err := io.WriteString(conn, `<proceed xmlns='urn:ietf:params:xml:ns:xmpp-tls'/>`); err != nil {
		return
	}
	server := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}})
	if err := server.Handshake(); err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, server)
}
