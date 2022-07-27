package cmd

import (
	"os"
	"os/exec"
	"syscall"
)

// Send the given signal to the process, returns an error if the process isn't
// running.
func signalProcess(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

func setProcessGroupID(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{}
}
