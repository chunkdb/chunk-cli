package chunkclient

import (
	"fmt"
	"unicode/utf8"
)

// replyField checks required fields while allowing additional map entries.
type replyField struct {
	name     string
	kind     Kind
	nullable bool
}

const (
	replyUint Kind = -1 - iota
	replyInt
	replyEpoch
)

func validateReplyFields(statement string, reply Value, fields ...replyField) error {
	if reply.Kind != KindMap {
		return fmt.Errorf("%s: expected a map, got %s", statement, reply.Kind)
	}
	for _, entry := range reply.Map {
		if entry.Key.Kind != KindBulk {
			return fmt.Errorf("%s: expected a bulk string map key", statement)
		}
	}
	for _, field := range fields {
		found := false
		for _, entry := range reply.Map {
			if string(entry.Key.Bulk) != field.name {
				continue
			}
			if found {
				return fmt.Errorf("%s: duplicate %s", statement, field.name)
			}
			found = true
			if !(field.nullable && entry.Value.Kind == KindNull || matchesReplyKind(entry.Value, field.kind)) {
				return fmt.Errorf("%s: invalid or missing %s", statement, field.name)
			}
		}
		if !found {
			return fmt.Errorf("%s: invalid or missing %s", statement, field.name)
		}
	}
	return nil
}

func matchesReplyKind(value Value, kind Kind) bool {
	switch kind {
	case replyUint:
		_, err := value.Uint64()
		return err == nil
	case replyInt:
		_, err := value.Int64()
		return err == nil
	case replyEpoch:
		return value.Kind == KindBulk && ValidEpoch(string(value.Bulk))
	default:
		return value.Kind == kind
	}
}

// ValidateOK checks the fixed acknowledgement shared by administrative and
// transaction statements.
func ValidateOK(statement string, reply Value) error {
	if reply.Kind != KindSimple || reply.Text != "OK" {
		return fmt.Errorf("%s: expected OK, got %s", statement, reply.Kind)
	}
	return nil
}

func ValidatePing(reply Value) error {
	if reply.Kind != KindSimple || reply.Text != "PONG" {
		return fmt.Errorf("PING: expected PONG, got %s", reply.Kind)
	}
	return nil
}

func ValidateRevision(statement string, reply Value, nullable bool) error {
	if nullable && reply.Kind == KindNull {
		return nil
	}
	if _, err := reply.Uint64(); err != nil {
		return fmt.Errorf("%s: invalid revision: %w", statement, err)
	}
	return nil
}

func ValidateMetrics(reply Value) error {
	if reply.Kind != KindBulk || !utf8.Valid(reply.Bulk) {
		return fmt.Errorf("SHOW METRICS: expected UTF-8 text")
	}
	return nil
}

func ValidateTables(reply Value) error {
	if reply.Kind != KindArray {
		return fmt.Errorf("SHOW TABLES: expected an array")
	}
	for _, table := range reply.Items {
		if table.Kind != KindBulk {
			return fmt.Errorf("SHOW TABLES: invalid table name")
		}
	}
	return nil
}

func ValidateUsers(reply Value) error {
	if reply.Kind != KindArray {
		return fmt.Errorf("SHOW USERS: expected an array")
	}
	for _, user := range reply.Items {
		if err := validateUser("SHOW USERS", user); err != nil {
			return err
		}
	}
	return nil
}

func validateUser(statement string, user Value) error {
	if err := validateReplyFields(statement, user,
		replyField{"name", KindBulk, false}, replyField{"manages_users", KindBool, false},
		replyField{"requires_channel_binding", KindBool, false}, replyField{"verifiers", replyUint, false},
		replyField{"grants", KindMap, false}, replyField{"server_rights", KindArray, false},
	); err != nil {
		return err
	}
	verifiers, _ := user.Lookup("verifiers")
	count, _ := verifiers.Uint64()
	if count != 1 && count != 2 {
		return fmt.Errorf("%s: invalid verifiers count", statement)
	}
	grants, _ := user.Lookup("grants")
	for _, grant := range grants.Map {
		if grant.Key.Kind != KindBulk || grant.Value.Kind != KindBulk {
			return fmt.Errorf("%s: invalid grant", statement)
		}
	}
	rights, _ := user.Lookup("server_rights")
	for _, right := range rights.Items {
		if right.Kind != KindBulk {
			return fmt.Errorf("%s: invalid server right", statement)
		}
	}
	return nil
}

func ValidateConnections(reply Value) error {
	const statement = "SHOW CONNECTIONS"
	if reply.Kind != KindArray {
		return fmt.Errorf("%s: expected an array", statement)
	}
	for _, connection := range reply.Items {
		if err := validateReplyFields(statement, connection,
			replyField{"id", replyUint, false}, replyField{"user", KindBulk, true},
			replyField{"peer", KindBulk, false}, replyField{"state", KindBulk, false},
			replyField{"statement", KindBulk, true}, replyField{"progress", KindMap, true},
		); err != nil {
			return err
		}
		if progress, _ := connection.Lookup("progress"); progress.Kind != KindNull {
			if err := validateReplyFields(statement, progress,
				replyField{"files_done", replyUint, false}, replyField{"files_total", replyUint, false},
				replyField{"bytes_done", replyUint, false}, replyField{"bytes_total", replyUint, false},
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// ParseConnectionControl returns whether the server accepted CANCEL or KILL.
func ParseConnectionControl(statement, key string, reply Value) (bool, error) {
	if err := validateReplyFields(statement, reply, replyField{key, KindBool, false}); err != nil {
		return false, err
	}
	value, _ := reply.Lookup(key)
	return value.Bool, nil
}

func ValidateBackup(reply Value) error {
	const statement = "BACKUP TO"
	if err := validateReplyFields(statement, reply,
		replyField{"tables", replyUint, false}, replyField{"files", replyUint, false},
		replyField{"bytes", replyUint, false}, replyField{"cuts", KindArray, false},
	); err != nil {
		return err
	}
	tables, _ := reply.Lookup("tables")
	count, _ := tables.Uint64()
	cuts, _ := reply.Lookup("cuts")
	if count != uint64(len(cuts.Items)) {
		return fmt.Errorf("%s: table count and cuts disagree", statement)
	}
	for _, cut := range cuts.Items {
		if err := validateReplyFields(statement, cut,
			replyField{"table", KindBulk, false}, replyField{"epoch", replyEpoch, false},
			replyField{"revision", replyUint, false},
		); err != nil {
			return err
		}
	}
	return nil
}

func ValidateRestoreTable(reply Value) error {
	return validateReplyFields("RESTORE TABLE", reply,
		replyField{"table", KindBulk, false}, replyField{"epoch", replyEpoch, false},
		replyField{"parent_epoch", replyEpoch, false}, replyField{"fork_revision", replyUint, false},
		replyField{"files", replyUint, false}, replyField{"bytes", replyUint, false},
	)
}

func ValidateMigration(reply Value) error {
	if reply.Kind != KindSimple || reply.Text != "applied" && reply.Text != "skipped" {
		return fmt.Errorf("expected applied or skipped simple string")
	}
	return nil
}

func ValidateMigrations(reply Value) error {
	const statement = "SHOW MIGRATIONS"
	if reply.Kind != KindArray {
		return fmt.Errorf("%s: expected an array of records", statement)
	}
	for _, record := range reply.Items {
		if record.Kind != KindMap {
			return fmt.Errorf("%s: expected a record map", statement)
		}
		if err := validateReplyFields(statement, record,
			replyField{"name", KindBulk, false}, replyField{"applied_ms", replyInt, false},
			replyField{"user", KindBulk, true}, replyField{"statement", KindBulk, false},
		); err != nil {
			return err
		}
	}
	return nil
}

func ValidateNull(statement string, reply Value) error {
	if reply.Kind != KindNull {
		return fmt.Errorf("%s: expected a null transaction reply, got %s", statement, reply.Kind)
	}
	return nil
}

// ParseCatalogRecord checks a known catalog entry and returns the schema of a table record.
func ParseCatalogRecord(kind, action, name string, table, data Value) (*Schema, error) {
	const statement = "WATCH CATALOG"
	switch kind {
	case "table", "user", "slot":
		if name == "" || (kind == "slot" && table.Kind != KindBulk) || (kind != "slot" && table.Kind != KindNull) {
			return nil, fmt.Errorf("%s: invalid %s name or table", statement, kind)
		}
	default:
		return nil, nil
	}
	switch action {
	case "drop":
		if data.Kind != KindNull {
			return nil, fmt.Errorf("%s: expected null data for drop", statement)
		}
		return nil, nil
	case "create", "alter":
	default:
		return nil, fmt.Errorf("%s: invalid %s action", statement, kind)
	}
	switch kind {
	case "table":
		schema, err := parseTableSchema(data, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", statement, err)
		}
		if schema.Table != name {
			return nil, fmt.Errorf("%s: table record has a different name", statement)
		}
		return schema, nil
	case "user":
		if err := validateUser(statement, data); err != nil {
			return nil, err
		}
		user, _ := data.Lookup("name")
		if string(user.Bulk) != name {
			return nil, fmt.Errorf("%s: user record has a different name", statement)
		}
	case "slot":
		if err := validateReplyFields(statement, data, replyField{"owner", KindBulk, true}, replyField{"filters", KindMap, false}); err != nil {
			return nil, err
		}
		filters, _ := data.Lookup("filters")
		return nil, validateSlotFilters(statement, filters)
	}
	return nil, nil
}
