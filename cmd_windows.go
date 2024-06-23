package cmd

import (
	"os"
	"os/exec"
	"syscall"
)

// Stop stops the command by sending its process group a SIGTERM signal.
// Stop is idempotent. An error should only be returned in the rare case that
// Stop is called immediately after the command ends but before Start can
// update its internal state.
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func setProcessGroupID(cmd *exec.Cmd) {
	// Set create new process group so the cmd and all its children become a new
	// process group. This allows Stop to SIGTERM the cmd's process group
	// without killing this process (i.e. this code here).
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
