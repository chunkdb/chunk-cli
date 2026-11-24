package main

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

const maxRetries = 5

// retryPause bounds reconnect and transaction backoff and remains interruptible.
func retryPause(ctx context.Context, attempt int) error {
	return pause(ctx, 25*time.Millisecond<<min(attempt, 4))
}

// maxRetryAfter caps one wait for a server's retry_after_ms.
const maxRetryAfter = 10 * time.Second

// serverRetryPause waits the retry_after_ms the server named, at most
// maxRetryAfter, or the backoff of the attempt when it named none.
func serverRetryPause(ctx context.Context, attempt int, server *chunkclient.ServerError) error {
	delay, named := retryAfter(server)
	if !named {
		return retryPause(ctx, attempt)
	}
	return pause(ctx, delay)
}

// retryAfter is the server's retry_after_ms, capped at maxRetryAfter.
func retryAfter(server *chunkclient.ServerError) (time.Duration, bool) {
	ms, named := server.UintKey("retry_after_ms")
	if !named {
		return 0, false
	}
	return time.Duration(min(ms, uint64(maxRetryAfter/time.Millisecond))) * time.Millisecond, true
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func transportFailure(err error) bool {
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
