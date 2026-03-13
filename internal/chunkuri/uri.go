package chunkuri

import (
	"fmt"
	"net"
	neturl "net/url"
	"strconv"
)

const DefaultPort = 4242

type Parsed struct {
	Scheme string
	Host   string
	Port   int
	Token  string
	Secure bool
}

func Parse(raw string) (Parsed, error) {
	u, err := neturl.Parse(raw)
	if err != nil {
		return Parsed{}, fmt.Errorf("parse uri: %w", err)
	}

	if u.Scheme != "chunk" && u.Scheme != "chunks" {
		return Parsed{}, fmt.Errorf("unsupported scheme %q (use chunk:// or chunks://)", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return Parsed{}, fmt.Errorf("missing host in uri")
	}

	port := DefaultPort
	if p := u.Port(); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return Parsed{}, fmt.Errorf("invalid port %q", p)
		}
		port = parsed
	}

	token := ""
	if u.User != nil {
		token = u.User.Username()
	}

	return Parsed{
		Scheme: u.Scheme,
		Host:   host,
		Port:   port,
		Token:  token,
		Secure: u.Scheme == "chunks",
	}, nil
}

func (p Parsed) Address() string {
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}
