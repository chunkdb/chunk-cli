package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// cancelOnInterrupt sends CANCEL for the running statement from a second
// connection when Ctrl-C arrives, so the server stops it and answers
// CANCELLED; a statement past its decision point still completes. A second
// Ctrl-C exits at once. The returned function stops listening.
func cancelOnInterrupt(client *chunkclient.Client, reconnect func() (*chunkclient.Client, error), stderr io.Writer) func() {
	info := client.Info()
	if info == nil || info.ConnectionID == 0 {
		return func() {}
	}
	id := info.ConnectionID
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-interrupts:
		}
		fmt.Fprintln(stderr, "cancelling the statement; press Ctrl-C again to quit")
		go func() {
			select {
			case <-done:
			case <-interrupts:
				os.Exit(130)
			}
		}()
		canceller, err := reconnect()
		if err != nil {
			fmt.Fprintf(stderr, "cancel: %v\n", err)
			return
		}
		defer func() { _ = canceller.Close() }()
		reply, err := canceller.Do("CANCEL " + strconv.FormatUint(id, 10))
		if err != nil {
			fmt.Fprintf(stderr, "cancel: %v\n", err)
			return
		}
		if cancelled, ok := reply.Lookup("cancelled"); ok && cancelled.Kind == chunkclient.KindBool && !cancelled.Bool {
			fmt.Fprintln(stderr, "cancel: the statement could no longer be cancelled")
		}
	}()
	return func() {
		signal.Stop(interrupts)
		close(done)
	}
}
