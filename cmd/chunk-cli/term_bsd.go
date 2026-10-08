//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package main

import "syscall"

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)
