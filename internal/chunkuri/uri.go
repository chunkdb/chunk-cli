package chunkuri

import (
	"fmt"
	"net"
	neturl "net/url"
	"strconv"
	"strings"
)

const DefaultPort = 4242

type Parsed struct {
	Scheme string
	Host   string
	Port   int
	Token  string
	Secure bool
	// Table is the table the path names (chunk://host:4242/terrain), empty
	// for / (the server's default table).
	Table string
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

	table := strings.TrimPrefix(u.Path, "/")
	if strings.Contains(table, "/") {
		return Parsed{}, fmt.Errorf("uri path must name one table: %q", u.Path)
	}

	return Parsed{
		Scheme: u.Scheme,
		Host:   host,
		Port:   port,
		Token:  token,
		Secure: u.Scheme == "chunks",
		Table:  table,
	}, nil
}

func (p Parsed) Address() string {
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}
