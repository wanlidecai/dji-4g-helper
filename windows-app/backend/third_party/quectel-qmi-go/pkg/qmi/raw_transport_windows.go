//go:build windows

package qmi

import "fmt"

func openRawTransport(path string) (qmiTransport, error) {
	return nil, fmt.Errorf("raw QMI device files are unsupported on Windows: %s; use DJI USB AT", path)
}
