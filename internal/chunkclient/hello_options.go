package chunkclient

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func normalizeSessionOptions(options map[string]Value) (map[string]Value, error) {
	result := make(map[string]Value, len(options))
	for key, value := range options {
		name := strings.ToLower(key)
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" || name[0] >= '0' && name[0] <= '9' {
			return nil, fmt.Errorf("HELLO: invalid option name %q", key)
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("HELLO: duplicate option %s", name)
		}
		switch value.Kind {
		case KindBulk, KindNull, KindBool:
		case KindInteger, KindBigNumber:
			if _, err := value.BigInt(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("HELLO: unsupported value for %s", name)
		}
		switch name {
		case "client_name":
			if value.Kind != KindBulk || !utf8.Valid(value.Bulk) {
				return nil, fmt.Errorf("HELLO: client_name needs UTF-8 text")
			}
		case "default_tag":
			if value.Kind != KindBulk || len(value.Bulk) > 256 {
				return nil, fmt.Errorf("HELLO: default_tag needs at most 256 bytes")
			}
		case "statement_timeout_ms":
			if _, err := value.Uint64(); err != nil {
				return nil, fmt.Errorf("HELLO: statement_timeout_ms needs unsigned milliseconds")
			}
		}
		if value.Kind == KindBulk {
			value.Bulk = append([]byte{}, value.Bulk...)
		}
		result[name] = value
	}
	return result, nil
}

func (c *Client) helloRequest(line string, parameters [][]byte) Request {
	names := make([]string, 0, len(c.sessionOptions))
	for name := range c.sessionOptions {
		names = append(names, name)
	}
	sort.Strings(names)
	assignments := make([]string, 0, len(names))
	for _, name := range names {
		value := c.sessionOptions[name]
		var literal string
		switch value.Kind {
		case KindBulk:
			parameters = append(parameters, value.Bulk)
			literal = "$" + strconv.Itoa(len(parameters))
		case KindNull:
			literal = "NULL"
		case KindBool:
			literal = strconv.FormatBool(value.Bool)
		default:
			literal = value.Text
		}
		assignments = append(assignments, name+" = "+literal)
	}
	if len(assignments) != 0 {
		line += " WITH " + strings.Join(assignments, ", ")
	}
	return Request{Statement: line, Parameters: parameters}
}

// Supports checks protocol capabilities, not enabled storage features.
func (i *ServerInfo) Supports(feature string) bool {
	for _, name := range i.Features {
		if name == feature {
			return true
		}
	}
	return false
}

func parseHelloExtensions(reply Value, info *ServerInfo) error {
	bad := func(key string) error { return fmt.Errorf("invalid HELLO %s", key) }
	if features, found := reply.Lookup("features"); found {
		if features.Kind != KindArray {
			return bad("features")
		}
		for _, feature := range features.Items {
			if feature.Kind != KindBulk || !utf8.Valid(feature.Bulk) {
				return bad("feature")
			}
			info.Features = append(info.Features, string(feature.Bulk))
		}
	}
	required := info.Supports("protocol_foundation")
	for key, dest := range map[string]*uint64{"connection_id": &info.ConnectionID, "idle_timeout_ms": &info.IdleTimeoutMS} {
		value, found := reply.Lookup(key)
		if !found {
			if required {
				return bad(key)
			}
			continue
		}
		number, err := value.Uint64()
		if err != nil || key == "connection_id" && number == 0 {
			return bad(key)
		}
		*dest = number
	}
	for _, key := range []string{"options", "not_applied", "limits"} {
		value, found := reply.Lookup(key)
		if !found {
			if required {
				return bad(key)
			}
			continue
		}
		if value.Kind != KindMap {
			return bad(key)
		}
		switch key {
		case "options":
			info.Options = map[string]Value{}
			for _, pair := range value.Map {
				if pair.Key.Kind != KindBulk {
					return bad("options key")
				}
				name := string(pair.Key.Bulk)
				if _, dup := info.Options[name]; dup {
					return bad("duplicate option")
				}
				if name == "client_name" || name == "default_tag" || name == "statement_timeout_ms" {
					if _, err := normalizeSessionOptions(map[string]Value{name: pair.Value}); err != nil {
						return err
					}
				}
				info.Options[name] = pair.Value
			}
		case "not_applied":
			info.NotApplied = map[string]string{}
			for _, pair := range value.Map {
				if pair.Key.Kind != KindBulk || pair.Value.Kind != KindMap {
					return bad("not_applied entry")
				}
				reason, found := pair.Value.Lookup("reason")
				if !found || reason.Kind != KindBulk {
					return bad("not_applied reason")
				}
				name := string(pair.Key.Bulk)
				if _, dup := info.NotApplied[name]; dup {
					return bad("duplicate not_applied")
				}
				info.NotApplied[name] = string(reason.Bulk)
			}
		case "limits":
			info.Limits = map[string]uint64{}
			for _, name := range []string{"max_area_chunks", "max_area_response_bytes", "txn_max_written_chunks", "txn_max_read_chunks", "max_watches", "max_returning_rows", "max_expression_nodes"} {
				raw, found := value.Lookup(name)
				if !found {
					if required {
						return bad(name)
					}
					continue
				}
				number, err := raw.Uint64()
				if err != nil {
					return bad(name)
				}
				info.Limits[name] = number
			}
		}
	}
	return nil
}
