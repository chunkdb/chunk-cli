//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package main

import (
	"errors"
	"io"
	"os"
)

// Without a known terminal interface no password is asked for; it comes from
// --password-file, CHUNKDB_PASSWORD or the URI.
func isTerminal(*os.File) bool {
	return false
}

func readPasswordNoEcho(*os.File, string, io.Writer) (string, error) {
	return "", errors.New("this system cannot read a password from the terminal")
}
