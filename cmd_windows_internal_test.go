//go:build windows

package cmd

import (
	"testing"
)

func TestTerminateProcess(t *testing.T) {
	err := signalProcess(123, syscall.SIGTERM)
	if err == nil {
		t.Error("no error, expected one on terminating nonexisting PID")
	}
}
