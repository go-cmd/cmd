//go:build windows

package cmd

import (
	"testing"
)

func TestTerminateProcess(t *testing.T) {
	err := terminateProcess(123)
	if err == nil {
		t.Error("no error, expected one on terminating nonexisting PID")
	}
}
