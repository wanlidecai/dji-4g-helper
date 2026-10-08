package qmi

import (
	"time"
)

const (
	defaultProxyPath        = "qmi-proxy"
	defaultProxyExecutable  = "/usr/libexec/qmi-proxy"
	defaultProxyOpenTimeout = 5 * time.Second
)

type qmiTransport interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	SetReadDeadline(time.Time) error
}


var (
	openRawTransportHook   = openRawTransport
	openProxyTransportHook = openProxyTransport
)
