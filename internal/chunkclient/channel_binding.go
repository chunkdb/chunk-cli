package chunkclient

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
)

// ChannelBinding says whether a login is bound to its TLS connection, so
// that a login relayed by something that ends TLS in between fails.
type ChannelBinding string

const (
	// ChannelBindingAuto binds every login made over TLS; the zero value
	// means the same.
	ChannelBindingAuto ChannelBinding = "auto"
	// ChannelBindingOff never binds: for TLS that ends at a load balancer
	// in front of the server.
	ChannelBindingOff ChannelBinding = "off"
	// ChannelBindingRequire binds like ChannelBindingAuto and refuses to
	// log in over a connection that is not TLS.
	ChannelBindingRequire ChannelBinding = "require"
)

// The channel binding types of a login (RFC 9266, RFC 5929).
const (
	bindingTLSExporter       = "tls-exporter"
	bindingTLSServerEndPoint = "tls-server-end-point"
)

// ParseChannelBinding reads auto, off or require.
func ParseChannelBinding(text string) (ChannelBinding, error) {
	switch mode := ChannelBinding(text); mode {
	case ChannelBindingAuto, ChannelBindingOff, ChannelBindingRequire:
		return mode, nil
	}
	return "", fmt.Errorf("channel binding is auto, off or require, got %q", text)
}

// loginBinding returns the channel binding type and data of a login on conn:
// tls-exporter on TLS 1.3, tls-server-end-point on TLS 1.2, and an empty
// type for a login that is not bound.
func loginBinding(conn net.Conn, mode ChannelBinding) (bindingType string, data []byte, err error) {
	if mode == ChannelBindingOff {
		return "", nil, nil
	}
	secure, ok := conn.(*tls.Conn)
	if !ok {
		if mode == ChannelBindingRequire {
			return "", nil, errors.New("channel binding is required, and this connection is not TLS; use a chunks:// URI")
		}
		return "", nil, nil
	}
	state := secure.ConnectionState()
	if state.Version >= tls.VersionTLS13 {
		data, err = state.ExportKeyingMaterial("EXPORTER-Channel-Binding", nil, 32)
		if err != nil {
			return "", nil, fmt.Errorf("read the TLS exporter for channel binding: %w", err)
		}
		return bindingTLSExporter, data, nil
	}
	if len(state.PeerCertificates) == 0 {
		return "", nil, errors.New("the server presented no certificate to bind the login to")
	}
	data, err = tlsServerEndPoint(state.PeerCertificates[0])
	if err != nil {
		return "", nil, err
	}
	return bindingTLSServerEndPoint, data, nil
}

// tlsServerEndPoint hashes the certificate with the hash of its signature
// algorithm, SHA-256 in place of MD5 and SHA-1 (RFC 5929 section 4.1).
func tlsServerEndPoint(certificate *x509.Certificate) ([]byte, error) {
	var hash crypto.Hash
	switch certificate.SignatureAlgorithm {
	case x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.ECDSAWithSHA1,
		x509.SHA256WithRSA, x509.DSAWithSHA256, x509.ECDSAWithSHA256, x509.SHA256WithRSAPSS:
		hash = crypto.SHA256
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		hash = crypto.SHA384
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		hash = crypto.SHA512
	default:
		return nil, fmt.Errorf(
			"the server certificate is signed with %s, which gives no channel binding over TLS 1.2; connect with TLS 1.3, or turn channel binding off",
			certificate.SignatureAlgorithm)
	}
	digest := hash.New()
	digest.Write(certificate.Raw)
	return digest.Sum(nil), nil
}
