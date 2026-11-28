package chunkclient

import (
	"errors"
	"fmt"
	"math"
)

// ValidateStatus checks known fields without rejecting extension keys/bits.
func ValidateStatus(reply Value) error {
	if reply.Kind != KindMap {
		return errors.New("SHOW STATUS: expected map")
	}
	directory, found := reply.Lookup("directory")
	if !found {
		return errors.New("SHOW STATUS: missing directory")
	}
	if err := validateFeatureFlags(directory); err != nil {
		return err
	}
	tables, found := reply.Lookup("tables")
	if !found || tables.Kind != KindArray {
		return errors.New("SHOW STATUS: invalid tables")
	}
	for _, table := range tables.Items {
		name, found := table.Lookup("name")
		if !found || name.Kind != KindBulk {
			return errors.New("SHOW STATUS: invalid table name")
		}
		if err := validateFeatureFlags(table); err != nil {
			return err
		}
		if state, found := table.Lookup("state"); found && state.Kind != KindBulk {
			return errors.New("SHOW STATUS: invalid table state")
		}
		if reason, found := table.Lookup("reason"); found && reason.Kind != KindBulk && reason.Kind != KindNull {
			return errors.New("SHOW STATUS: invalid table reason")
		}
	}
	pending, found := reply.Lookup("activation_pending")
	if !found || pending.Kind != KindBool {
		return errors.New("SHOW STATUS: invalid activation_pending")
	}
	// Servers before 2.0 omit the server fields; when present they are typed.
	if version, found := reply.Lookup("version"); found && version.Kind != KindBulk {
		return errors.New("SHOW STATUS: invalid version")
	}
	if uptime, found := reply.Lookup("uptime_ms"); found {
		if _, err := uptime.Uint64(); err != nil {
			return errors.New("SHOW STATUS: invalid uptime_ms")
		}
	}
	if readOnly, found := reply.Lookup("read_only"); found && readOnly.Kind != KindBool {
		return errors.New("SHOW STATUS: invalid read_only")
	}
	if fenced, found := reply.Lookup("fenced"); found {
		if fenced.Kind != KindArray {
			return errors.New("SHOW STATUS: invalid fenced")
		}
		for _, table := range fenced.Items {
			name, nameFound := table.Lookup("name")
			reason, reasonFound := table.Lookup("reason")
			if table.Kind != KindMap || !nameFound || name.Kind != KindBulk || !reasonFound || reason.Kind != KindBulk {
				return errors.New("SHOW STATUS: invalid fenced table")
			}
		}
	}
	return nil
}

// ValidateTableStats checks the counts of SHOW TABLE STATS without rejecting
// extension keys.
func ValidateTableStats(reply Value) error {
	if reply.Kind != KindMap {
		return errors.New("SHOW TABLE STATS: expected map")
	}
	epoch, found := reply.Lookup("epoch")
	if !found || epoch.Kind != KindBulk || !ValidEpoch(string(epoch.Bulk)) {
		return errors.New("SHOW TABLE STATS: invalid epoch")
	}
	revision, found := reply.Lookup("revision")
	if !found {
		return errors.New("SHOW TABLE STATS: missing revision")
	}
	if revision.Kind != KindNull {
		if _, err := revision.Uint64(); err != nil {
			return errors.New("SHOW TABLE STATS: invalid revision")
		}
	}
	dims, found := reply.Lookup("dims")
	if n, err := dims.Uint64(); !found || err != nil || n != 2 && n != 3 {
		return errors.New("SHOW TABLE STATS: invalid dims")
	}
	for _, key := range []string{"chunks", "tombstones", "retained_deletion_floor", "loaded_chunks", "files", "disk_bytes", "slots", "watches"} {
		value, found := reply.Lookup(key)
		if !found {
			return fmt.Errorf("SHOW TABLE STATS: missing %s", key)
		}
		if _, err := value.Uint64(); err != nil {
			return fmt.Errorf("SHOW TABLE STATS: invalid %s", key)
		}
	}
	return nil
}

func validateFeatureFlags(flags Value) error {
	if flags.Kind != KindMap {
		return errors.New("SHOW STATUS: invalid feature map")
	}
	for _, key := range []string{"compat", "ro_compat", "incompat"} {
		value, found := flags.Lookup(key)
		if !found {
			return fmt.Errorf("SHOW STATUS: missing %s", key)
		}
		if _, err := value.Uint64(); err != nil {
			return fmt.Errorf("SHOW STATUS: invalid %s", key)
		}
	}
	enabled, found := flags.Lookup("enabled")
	if !found || enabled.Kind != KindArray {
		return errors.New("SHOW STATUS: invalid enabled")
	}
	for _, name := range enabled.Items {
		if name.Kind != KindBulk {
			return errors.New("SHOW STATUS: invalid feature name")
		}
	}
	return nil
}

// ValidateSlots validates the persisted filters, including nullable renamed-away
// column names, and the nullable owner. Older replies without filters or owner
// remain readable.
func ValidateSlots(reply Value) error {
	if reply.Kind != KindArray {
		return errors.New("SHOW SLOTS: expected array")
	}
	for _, slot := range reply.Items {
		if slot.Kind != KindMap {
			return errors.New("SHOW SLOTS: invalid slot")
		}
		for _, key := range []string{"acked", "retained_bytes"} {
			value, found := slot.Lookup(key)
			if !found {
				return fmt.Errorf("SHOW SLOTS: missing %s", key)
			}
			if _, err := value.Uint64(); err != nil {
				return err
			}
		}
		for _, key := range []string{"table", "name", "epoch"} {
			value, found := slot.Lookup(key)
			if !found || value.Kind != KindBulk || key == "epoch" && !ValidEpoch(string(value.Bulk)) {
				return fmt.Errorf("SHOW SLOTS: invalid %s", key)
			}
		}
		// revision, lag_revisions and attached came with 2.0.
		for _, key := range []string{"revision", "lag_revisions"} {
			if value, found := slot.Lookup(key); found {
				if _, err := value.Uint64(); err != nil {
					return fmt.Errorf("SHOW SLOTS: invalid %s", key)
				}
			}
		}
		if attached, found := slot.Lookup("attached"); found && attached.Kind != KindBool {
			return errors.New("SHOW SLOTS: invalid attached")
		}
		if peer, found := slot.Lookup("peer"); found && peer.Kind != KindNull && peer.Kind != KindBulk {
			return errors.New("SHOW SLOTS: invalid peer")
		}
		if lastACK, found := slot.Lookup("last_ack_ms"); found && lastACK.Kind != KindNull {
			if _, err := lastACK.Uint64(); err != nil {
				return errors.New("SHOW SLOTS: invalid last_ack_ms")
			}
		}
		lost, found := slot.Lookup("lost")
		if !found || lost.Kind != KindBool {
			return errors.New("SHOW SLOTS: invalid lost")
		}
		if reason, found := slot.Lookup("lost_reason"); found && reason.Kind != KindNull && reason.Kind != KindBulk {
			return errors.New("SHOW SLOTS: invalid lost_reason")
		}
		if owner, found := slot.Lookup("owner"); found && owner.Kind != KindNull && owner.Kind != KindBulk {
			return errors.New("SHOW SLOTS: invalid owner")
		}
		filters, found := slot.Lookup("filters")
		if !found {
			continue
		}
		if err := validateSlotFilters("SHOW SLOTS", filters); err != nil {
			return err
		}
	}
	return nil
}

func validateSlotFilters(statement string, filters Value) error {
	if filters.Kind != KindMap {
		return fmt.Errorf("%s: invalid filters", statement)
	}
	for _, key := range []string{"no_before", "chunks"} {
		value, found := filters.Lookup(key)
		if !found || value.Kind != KindBool {
			return fmt.Errorf("%s: invalid %s", statement, key)
		}
	}
	area, found := filters.Lookup("area")
	if !found {
		return fmt.Errorf("%s: missing area", statement)
	}
	if area.Kind != KindNull {
		if area.Kind != KindArray || len(area.Items) != 4 && len(area.Items) != 6 {
			return fmt.Errorf("%s: invalid area", statement)
		}
		dims := len(area.Items) / 2
		for axis := 0; axis < dims; axis++ {
			lo, e1 := area.Items[axis].Int64()
			hi, e2 := area.Items[axis+dims].Int64()
			if e1 != nil || e2 != nil || lo > hi {
				return fmt.Errorf("%s: invalid area bounds", statement)
			}
		}
	}
	columns, cFound := filters.Lookup("columns")
	ids, iFound := filters.Lookup("column_ids")
	if !cFound || !iFound {
		return fmt.Errorf("%s: missing columns/column_ids", statement)
	}
	if columns.Kind == KindNull && ids.Kind == KindNull {
		return nil
	}
	if columns.Kind != KindArray || ids.Kind != KindArray || len(columns.Items) != len(ids.Items) {
		return fmt.Errorf("%s: unaligned columns", statement)
	}
	for i, name := range columns.Items {
		if name.Kind != KindBulk && name.Kind != KindNull {
			return fmt.Errorf("%s: invalid column name", statement)
		}
		id, err := ids.Items[i].Uint64()
		if err != nil || id > math.MaxUint32 {
			return fmt.Errorf("%s: invalid column id", statement)
		}
	}
	return nil
}
