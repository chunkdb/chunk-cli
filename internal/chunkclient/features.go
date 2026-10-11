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
	}
	pending, found := reply.Lookup("activation_pending")
	if !found || pending.Kind != KindBool {
		return errors.New("SHOW STATUS: invalid activation_pending")
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
// column names. Older replies without filters remain readable.
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
			if !found || value.Kind != KindBulk {
				return fmt.Errorf("SHOW SLOTS: invalid %s", key)
			}
		}
		lost, found := slot.Lookup("lost")
		if !found || lost.Kind != KindBool {
			return errors.New("SHOW SLOTS: invalid lost")
		}
		if reason, found := slot.Lookup("lost_reason"); found && reason.Kind != KindNull && reason.Kind != KindBulk {
			return errors.New("SHOW SLOTS: invalid lost_reason")
		}
		filters, found := slot.Lookup("filters")
		if !found {
			continue
		}
		if filters.Kind != KindMap {
			return errors.New("SHOW SLOTS: invalid filters")
		}
		for _, key := range []string{"no_before", "chunks"} {
			value, found := filters.Lookup(key)
			if !found || value.Kind != KindBool {
				return fmt.Errorf("SHOW SLOTS: invalid %s", key)
			}
		}
		area, found := filters.Lookup("area")
		if !found {
			return errors.New("SHOW SLOTS: missing area")
		}
		if area.Kind != KindNull {
			if area.Kind != KindArray || len(area.Items) != 4 && len(area.Items) != 6 {
				return errors.New("SHOW SLOTS: invalid area")
			}
			dims := len(area.Items) / 2
			for axis := 0; axis < dims; axis++ {
				lo, e1 := area.Items[axis].Int64()
				hi, e2 := area.Items[axis+dims].Int64()
				if e1 != nil || e2 != nil || lo > hi {
					return errors.New("SHOW SLOTS: invalid area bounds")
				}
			}
		}
		columns, cFound := filters.Lookup("columns")
		ids, iFound := filters.Lookup("column_ids")
		if !cFound || !iFound {
			return errors.New("SHOW SLOTS: missing columns/column_ids")
		}
		if columns.Kind == KindNull && ids.Kind == KindNull {
			continue
		}
		if columns.Kind != KindArray || ids.Kind != KindArray || len(columns.Items) != len(ids.Items) {
			return errors.New("SHOW SLOTS: unaligned columns")
		}
		for i, name := range columns.Items {
			if name.Kind != KindBulk && name.Kind != KindNull {
				return errors.New("SHOW SLOTS: invalid column name")
			}
			id, err := ids.Items[i].Uint64()
			if err != nil || id > math.MaxUint32 {
				return errors.New("SHOW SLOTS: invalid column id")
			}
		}
	}
	return nil
}
