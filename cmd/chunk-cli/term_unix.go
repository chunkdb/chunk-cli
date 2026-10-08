//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

func getTermios(fd uintptr) (syscall.Termios, error) {
	var state syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlGetTermios, uintptr(unsafe.Pointer(&state))); errno != 0 {
		return state, errno
	}
	return state, nil
}

func setTermios(fd uintptr, state *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlSetTermios, uintptr(unsafe.Pointer(state))); errno != 0 {
		return errno
	}
	return nil
}

func isTerminal(f *os.File) bool {
	_, err := getTermios(f.Fd())
	return err == nil
}

// readPasswordNoEcho turns echo off, writes the prompt and reads one line
// from the terminal; an interrupt turns echo back on before the process
// exits.
func readPasswordNoEcho(f *os.File, prompt string, out io.Writer) (string, error) {
	fd := f.Fd()
	saved, err := getTermios(fd)
	if err != nil {
		return "", fmt.Errorf("read the terminal settings: %w", err)
	}
	quiet := saved
	quiet.Lflag &^= syscall.ECHO
	quiet.Lflag |= syscall.ICANON | syscall.ISIG
	if err := setTermios(fd, &quiet); err != nil {
		return "", fmt.Errorf("turn off echo: %w", err)
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupted:
			_ = setTermios(fd, &saved)
			fmt.Fprintln(out)
			os.Exit(130)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(interrupted)
		close(done)
		_ = setTermios(fd, &saved)
		// The newline the user typed was not echoed.
		fmt.Fprintln(out)
	}()

	fmt.Fprint(out, prompt)

	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
			continue
		}
		if err != nil {
			// End of input (Ctrl-D) ends a typed line too.
			if len(line) == 0 {
				return "", errors.New("no password was typed")
			}
			break
		}
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return string(line), nil
}
