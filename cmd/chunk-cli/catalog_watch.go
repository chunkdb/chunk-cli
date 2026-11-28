package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// printCatalogEvent prints one push of WATCH CATALOG: a catalog event, a
// progress event, or a resync that the whole catalog follows.
func printCatalogEvent(w io.Writer, push chunkclient.Value, json bool) error {
	if chunkclient.UnknownPush(push) {
		return nil
	}
	if push.Kind != chunkclient.KindPush || len(push.Items) < 3 {
		return errors.New("WATCH CATALOG: invalid push")
	}
	kind, err := watchString(push.Items[0])
	if err != nil {
		return err
	}
	switch kind {
	case "catalog":
		return printWatchEvent(w, push, json, nil, nil)
	case "progress", "resync":
	default:
		return fmt.Errorf("WATCH CATALOG: unexpected %q event", kind)
	}
	epoch, err := watchString(push.Items[1])
	if err != nil {
		return err
	}
	revision, err := push.Items[2].Uint64()
	if err != nil {
		return err
	}
	position, err := parseWatchPosition(fmt.Sprintf("%s:%d", epoch, revision))
	if err != nil {
		return err
	}
	if json {
		return writeJSON(w, orderedMap{{"type", kind}, {"position", position}})
	}
	line := fmt.Sprintf("progress %s:%d\n", position.Epoch, position.Revision)
	if kind == "resync" {
		line = fmt.Sprintf("resync %s:%d; the whole catalog follows: start your catalog over from it\n", position.Epoch, position.Revision)
	}
	n, err := io.WriteString(w, line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	return err
}
