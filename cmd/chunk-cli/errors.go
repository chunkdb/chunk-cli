package main

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func printError(w io.Writer, err error, json bool) int {
	code := 1
	var server *chunkclient.ServerError
	if errors.As(err, &server) {
		code = server.ExitCode()
	}
	if !json {
		fmt.Fprintf(w, "error: %v\n", err)
		return code
	}
	name, class, message := "CLIENT_ERROR", "fatal", err.Error()
	keys := orderedMap{}
	if server != nil {
		name, class, message = server.Code, server.RetryClass(), server.Message
		names := make([]string, 0, len(server.Keys))
		for key := range server.Keys {
			names = append(names, key)
		}
		sort.Strings(names)
		for _, key := range names {
			var value any = server.Keys[key]
			switch key {
			case "current", "expected", "actual", "revision", "connection_id", "retry_after_ms":
				if number, valid := server.UintKey(key); valid {
					value = number
				}
			}
			keys = append(keys, jsonField{key, value})
		}
	}
	_ = writeJSON(w, orderedMap{{"error", orderedMap{{"code", name}, {"class", class}, {"keys", keys}, {"message", message}}}})
	return code
}
