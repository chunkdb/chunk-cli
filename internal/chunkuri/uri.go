package chunkuri

import (
	"fmt"
	"net"
	neturl "net/url"
	"strconv"
	"strings"
)

const DefaultPort = 4242

// form is the URI shape that the errors below point to.
const form = "chunk://user@host:4242/, or chunks:// for TLS"

type Parsed struct {
	Scheme string
	Host   string
	Port   int
	// User and Password are the login of chunk://user:password@host/,
	// percent-decoded.
	User     string
	Password string
	Secure   bool
	// Table is the table the path names (chunk://host:4242/terrain), empty
	// for / (the client's default table name).
	Table string
}

func Parse(raw string) (Parsed, error) {
	// Without a scheme the parser reports an unrelated path error, and its
	// text would repeat a password in the URI.
	if !strings.Contains(raw, "://") {
		return Parsed{}, fmt.Errorf("the uri has no scheme; add chunk:// as in %s", form)
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return Parsed{}, fmt.Errorf("parse uri: %w; use %s", err, form)
	}

	if u.Scheme != "chunk" && u.Scheme != "chunks" {
		return Parsed{}, fmt.Errorf("unsupported scheme %q; use %s", u.Scheme, form)
	}

	host := u.Hostname()
	if host == "" {
		return Parsed{}, fmt.Errorf("missing host in uri; use %s", form)
	}

	port := DefaultPort
	if p := u.Port(); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return Parsed{}, fmt.Errorf("invalid port %q; use a port from 1 to 65535, as in %s", p, form)
		}
		port = parsed
	}

	user, password, hasPassword := "", "", false
	if u.User != nil {
		user = u.User.Username()
		password, hasPassword = u.User.Password()
	}
	if user == "" && hasPassword {
		return Parsed{}, fmt.Errorf("the uri has a password but no user; use chunk://user:password@host:4242/")
	}

	table := strings.TrimPrefix(u.Path, "/")
	if strings.Contains(table, "/") {
		return Parsed{}, fmt.Errorf("uri path must name one table: %q; use chunk://user@host:4242/table", u.Path)
	}

	return Parsed{
		Scheme:   u.Scheme,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Secure:   u.Scheme == "chunks",
		Table:    table,
	}, nil
}

func (p Parsed) Address() string {
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}
