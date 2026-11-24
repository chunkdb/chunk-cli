package main

import (
	"context"
	"errors"
	"io"
	"math"
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

// serverRetryPause waits the retry_after_ms the server named, or the backoff
// of the attempt when it named none.
func serverRetryPause(ctx context.Context, attempt int, server *chunkclient.ServerError) error {
	ms, named := server.UintKey("retry_after_ms")
	if !named {
		return retryPause(ctx, attempt)
	}
	return pause(ctx, time.Duration(min(ms, uint64(math.MaxInt64/int64(time.Millisecond))))*time.Millisecond)
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
