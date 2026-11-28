package main

import (
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// validateStatementReply checks replies that use the common text and JSON
// formatter. Schema-dependent replies have their own renderers.
func validateStatementReply(statement string, words []string, info statementInfo, reply chunkclient.Value, inTxn bool) error {
	if info.kind == statementScanIndex {
		_, err := validateScanPage(info, reply, nil)
		return err
	}
	if len(words) == 0 {
		return nil
	}
	verb, _, _ := strings.Cut(words[0], "'")
	if strings.EqualFold(verb, "SET") || strings.EqualFold(verb, "DELETE") {
		if inTxn {
			return chunkclient.ValidateNull(verb, reply)
		}
		return chunkclient.ValidateRevision(verb, reply, false)
	}
	switch strings.ToUpper(verb) {
	case "PING":
		return chunkclient.ValidatePing(reply)
	case "COMPACT":
		return chunkclient.ValidateRevision("COMPACT TOMBSTONES", reply, false)
	case "CREATE", "ALTER", "DROP", "RECOVER", "SWAP", "ENABLE", "GRANT", "REVOKE", "FLUSH", "RELOAD", "UNWATCH":
		return chunkclient.ValidateOK(verb, reply)
	case "BACKUP":
		return chunkclient.ValidateBackup(reply)
	case "RESTORE":
		return chunkclient.ValidateRestoreTable(reply)
	case "MIGRATE":
		return chunkclient.ValidateMigration(reply)
	case "CANCEL":
		_, err := chunkclient.ParseConnectionControl("CANCEL", "cancelled", reply)
		return err
	case "KILL":
		_, err := chunkclient.ParseConnectionControl("KILL CONNECTION", "killed", reply)
		return err
	case "AGGREGATE":
		return validateAggregate(statement, reply)
	case "SHOW":
		if len(words) < 2 {
			return nil
		}
		switch strings.ToUpper(words[1]) {
		case "TABLES":
			return chunkclient.ValidateTables(reply)
		case "USERS":
			return chunkclient.ValidateUsers(reply)
		case "STATUS":
			return chunkclient.ValidateStatus(reply)
		case "TABLE":
			return chunkclient.ValidateTableStats(reply)
		case "SLOTS":
			return chunkclient.ValidateSlots(reply)
		case "MIGRATIONS":
			return chunkclient.ValidateMigrations(reply)
		case "CONNECTIONS":
			return chunkclient.ValidateConnections(reply)
		case "METRICS":
			return chunkclient.ValidateMetrics(reply)
		}
	}
	return nil
}
