package cmd

import (
	"os/exec"
	"syscall"
)

func signalProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func setProcessGroupID(cmd *exec.Cmd) {
	// Set process group ID so the cmd and all its children become a new
	// process group. This allows Stop to SIGTERM the cmd's process group
	// without killing this process (i.e. this code here).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
