//go:build smoke

package smoke

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// xmppSession is the smallest client that produces a real c2s session: a
// plaintext stream, SASL PLAIN, resource binding and initial presence. It
// exists so the smoke test can watch a session appear, be listed and be
// terminated by the adapter rather than by the client.
type xmppSession struct {
	conn net.Conn
	r    *bufio.Reader
	JID  string
}

func connectXMPP(t *testing.T, addr, domain, localpart, password, resource string) (*xmppSession, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	s := &xmppSession{conn: conn, r: bufio.NewReader(conn)}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	fail := func(err error) (*xmppSession, error) {
		_ = conn.Close()
		return nil, err
	}

	if err := s.openStream(domain); err != nil {
		return fail(err)
	}
	if _, err := s.readUntil("</stream:features>", "<stream:features/>"); err != nil {
		return fail(fmt.Errorf("features: %w", err))
	}
	auth := base64.StdEncoding.EncodeToString([]byte("\x00" + localpart + "\x00" + password))
	if err := s.write(`<auth xmlns='urn:ietf:params:xml:ns:xmpp-sasl' mechanism='PLAIN'>` + auth + `</auth>`); err != nil {
		return fail(err)
	}
	reply, err := s.readUntil("<success", "<failure", "</failure>")
	if err != nil {
		return fail(fmt.Errorf("sasl: %w", err))
	}
	if strings.Contains(reply, "<failure") {
		if !strings.Contains(reply, "</failure>") {
			more, _ := s.readUntil("</failure>")
			reply += more
		}
		return fail(fmt.Errorf("authentication failed: %s", strings.TrimSpace(reply)))
	}
	if err := s.openStream(domain); err != nil {
		return fail(err)
	}
	if _, err := s.readUntil("</stream:features>"); err != nil {
		return fail(fmt.Errorf("features after auth: %w", err))
	}
	if err := s.write(`<iq type='set' id='bind1'><bind xmlns='urn:ietf:params:xml:ns:xmpp-bind'><resource>` + resource + `</resource></bind></iq>`); err != nil {
		return fail(err)
	}
	reply, err = s.readUntil("</iq>")
	if err != nil {
		return fail(fmt.Errorf("bind: %w", err))
	}
	start := strings.Index(reply, "<jid>")
	end := strings.Index(reply, "</jid>")
	if start < 0 || end < start {
		return fail(fmt.Errorf("bind reply without jid: %s", reply))
	}
	s.JID = reply[start+len("<jid>") : end]
	if err := s.write(`<presence/>`); err != nil {
		return fail(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return s, nil
}

func (s *xmppSession) openStream(domain string) error {
	return s.write(`<?xml version='1.0'?><stream:stream to='` + domain + `' xmlns='jabber:client' xmlns:stream='http://etherx.jabber.org/streams' version='1.0'>`)
}

func (s *xmppSession) write(xml string) error {
	_, err := s.conn.Write([]byte(xml))
	return err
}

// readUntil accumulates bytes until one of the markers appears.
func (s *xmppSession) readUntil(markers ...string) (string, error) {
	var buf strings.Builder
	chunk := make([]byte, 4096)
	for {
		n, err := s.r.Read(chunk)
		buf.Write(chunk[:n])
		for _, m := range markers {
			if strings.Contains(buf.String(), m) {
				return buf.String(), nil
			}
		}
		if err != nil {
			return buf.String(), err
		}
	}
}

// waitClosed reports whether the server ended the stream within the timeout.
func (s *xmppSession) waitClosed(timeout time.Duration) bool {
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()
	chunk := make([]byte, 4096)
	for {
		n, err := s.r.Read(chunk)
		if err != nil {
			return true
		}
		if strings.Contains(string(chunk[:n]), "</stream:stream>") {
			return true
		}
	}
}

func (s *xmppSession) Close() {
	_ = s.write(`</stream:stream>`)
	_ = s.conn.Close()
}
