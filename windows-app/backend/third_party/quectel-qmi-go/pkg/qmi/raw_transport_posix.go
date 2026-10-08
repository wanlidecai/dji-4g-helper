//go:build !windows

package qmi

import (
	"fmt"
	"os"
	"syscall"
)

func openRawTransport(path string) (qmiTransport, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open QMI device %s: %w", path, err)
	}
	return f, nil
}
