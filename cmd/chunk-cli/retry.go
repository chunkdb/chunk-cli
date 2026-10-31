package main

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

const maxRetries = 5

// retryPause bounds reconnect and transaction backoff and remains interruptible.
func retryPause(ctx context.Context, attempt int) error {
	timer := time.NewTimer(25 * time.Millisecond << min(attempt, 4))
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
