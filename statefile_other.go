//go:build !darwin && !linux && !freebsd

package main

import (
	"errors"
	"os"
)

func openStateFile(string, bool) (*os.File, error) {
	return nil, errors.New("state persistence is qualified only on Unix")
}
