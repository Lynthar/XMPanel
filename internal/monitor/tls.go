package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// certificateAt reads the certificate a TCP endpoint serves and reports how
// long it stays valid. The handshake deliberately skips Go's verification so
// that an expired or untrusted chain still yields an expiry date to show;
// the chain is then verified separately and reported as its own error.
func certificateAt(ctx context.Context, address, serverName string, start starter) CheckItem {
	item := CheckItem{Target: address}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return failedItem(address, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if start != nil {
		if err := start(conn, serverName); err != nil {
			return failedItem(address, err)
		}
	}

	// Verification is done below instead of by the handshake, so an expired or
	// untrusted chain still yields the expiry date the operator needs to see.
	client := tls.Client(conn, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
	if err := client.HandshakeContext(ctx); err != nil {
		return failedItem(address, err)
	}
	state := client.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return failedItem(address, errors.New("server sent no certificate"))
	}

	leaf := state.PeerCertificates[0]
	item.NotAfter = &leaf.NotAfter
	item.Status = expiryStatus(leaf.NotAfter)
	item.Values = []string{leaf.Subject.CommonName}
	if err := verifyChain(state.PeerCertificates, serverName); err != nil && item.Status == StatusOK {
		item.Status, item.Error = StatusFail, err.Error()
	}
	return item
}

// verifyChain checks the served chain against the system roots for the name
// the caller expects, which is what a real client would do.
func verifyChain(certs []*x509.Certificate, serverName string) error {
	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: intermediates})
	return err
}

// starter negotiates whatever precedes the TLS handshake on a port that does
// not start in TLS. nil means the port speaks TLS immediately.
type starter func(conn net.Conn, domain string) error

func matrixTLSCheck(ctx context.Context, domain string) Check {
	federation, certName := matrixDelegation(ctx, domain)
	items := []CheckItem{certificateAt(ctx, net.JoinHostPort(domain, "443"), domain, nil)}
	// Skip the federation port when delegation points back at the client port:
	// checking the same endpoint twice would only duplicate the finding.
	if federation.String() != net.JoinHostPort(domain, "443") {
		items = append(items, certificateAt(ctx, federation.String(), certName, nil))
	}
	return checkResult(KindTLSCert, items...)
}

// xmppTLSCheck reads the certificates on the ports the network actually uses:
// the SRV targets where they exist, the default ports otherwise. Direct-TLS
// ports are checked only when advertised, because XEP-0368 says that is the
// only way a client learns of them.
func xmppTLSCheck(ctx context.Context, domain string) Check {
	var items []CheckItem
	for _, port := range []struct {
		service     string
		defaultPort uint16
		stream      string
	}{
		{"xmpp-client", 5222, streamClient},
		{"xmpp-server", 5269, streamServer},
	} {
		address := net.JoinHostPort(domain, fmt.Sprint(port.defaultPort))
		if targets, _, err := lookupSRV(ctx, port.service, domain); err == nil && len(targets) > 0 {
			address = targets[0].String()
		}
		items = append(items, certificateAt(ctx, address, domain, startTLS(port.stream)))
	}
	for _, service := range []string{"xmpps-client", "xmpps-server"} {
		targets, _, err := lookupSRV(ctx, service, domain)
		if err != nil || len(targets) == 0 {
			continue
		}
		items = append(items, certificateAt(ctx, targets[0].String(), domain, nil))
	}
	return checkResult(KindTLSCert, items...)
}

const (
	streamClient = "jabber:client"
	streamServer = "jabber:server"
)

// startTLS performs the XMPP STARTTLS upgrade of RFC 6120 §5.4. The reply is
// matched as raw bytes rather than parsed: an XMPP stream's root element stays
// open for the life of the connection, so a streaming XML decoder would block
// waiting for a close tag that only arrives at shutdown.
func startTLS(namespace string) starter {
	return func(conn net.Conn, domain string) error {
		open := fmt.Sprintf(
			`<?xml version='1.0'?><stream:stream to='%s' xmlns='%s' xmlns:stream='http://etherx.jabber.org/streams' version='1.0'>`,
			xmlAttr(domain), namespace)
		if _, err := io.WriteString(conn, open); err != nil {
			return err
		}
		if err := readUntil(conn, "<starttls"); err != nil {
			return fmt.Errorf("server did not offer STARTTLS: %w", err)
		}
		if _, err := io.WriteString(conn, `<starttls xmlns='urn:ietf:params:xml:ns:xmpp-tls'/>`); err != nil {
			return err
		}
		if err := readUntil(conn, "<proceed"); err != nil {
			return fmt.Errorf("STARTTLS was refused: %w", err)
		}
		return nil
	}
}

// xmlAttr keeps a domain that contains a quote from breaking out of the
// attribute it is written into.
func xmlAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&apos;", "<", "&lt;").Replace(value)
}

// readUntil reads until marker appears or the read budget runs out. The budget
// bounds a peer that answers with an endless stream of something else.
func readUntil(conn net.Conn, marker string) error {
	const budget = 16 * 1024
	buf := make([]byte, 2048)
	var seen strings.Builder
	for seen.Len() < budget {
		n, err := conn.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), marker) {
				return nil
			}
			if strings.Contains(seen.String(), "<stream:error") {
				return errors.New("server answered with a stream error")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("server closed the stream")
			}
			return err
		}
	}
	return errors.New("no answer within the read budget")
}
