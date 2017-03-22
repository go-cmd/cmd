// Package cmd provides higher-level wrappers around os/exec.Cmd. All operations
// are thread-safe and designed to be used asynchronously by multiple goroutines.
package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Cmd represents an external command, similar to the Go built-in os/exec.Cmd.
// A Cmd cannot be reused after calling Start.
type Cmd struct {
	Name string
	Args []string
	// --
	*sync.Mutex
	started   bool      // cmd.Start called, no error
	stopped   bool      // Stop called
	done      bool      // run() done
	final     bool      // status finalized in Status
	startTime time.Time // if started true
	stdout    *output
	stderr    *output
	status    Status
	doneChan  chan Status
}

// Status represents the status of a Cmd. It is valid during the entire lifecycle
// of the command. If StartTs > 0 (or PID > 0), the command has started. If
// StopTs > 0, the command has stopped. After the command has stopped, Exit = 0
// is usually enough to indicate success, but complete success is indicated by:
//   Exit     = 0
//   Error    = nil
//   Complete = true
// If Complete is false, the command was stopped or timed out. Error is a Go
// error related to starting or running the command.
type Status struct {
	Cmd      string
	PID      int
	Complete bool    // false if stopped or signaled
	Exit     int     // exit code of process
	Error    error   // Go error
	StartTs  int64   // Unix ts (nanoseconds)
	StopTs   int64   // Unix ts (nanoseconds)
	Runtime  float64 // seconds
	Stdout   []string
	Stderr   []string
}

// NewCmd creates a new Cmd for the given command name and arguments. The command
// is not started until Start is called.
func NewCmd(name string, args ...string) *Cmd {
	return &Cmd{
		Name: name,
		Args: args,
		// --
		Mutex: &sync.Mutex{},
		status: Status{
			Cmd:      name,
			PID:      0,
			Complete: false,
			Exit:     -1,
			Error:    nil,
			Runtime:  0,
		},
	}
}

// Start starts the command and immediately returns a channel that the caller
// can use to receive the final Status of the command when it ends. The caller
// can start the command and wait like,
//
//   status := <-c.Start() // blocking
//
// or start the command asynchronously and be notified later when it ends,
//
//   statusChan := c.Start() // non-blocking
//   // do other stuff...
//   status := <-statusChan // blocking
//
// Either way, exactly one Status is sent on the channel when the command ends.
// The channel is not closed. Any error is set to Status.Error. Start is idempotent;
// it always returns the same channel.
func (c *Cmd) Start() <-chan Status {
	c.Lock()
	defer c.Unlock()

	if c.doneChan != nil {
		return c.doneChan
	}

	c.doneChan = make(chan Status, 1)
	go c.run()
	return c.doneChan
}

// Stop stops the command by sending its process group a SIGTERM signal.
// Stop is idempotent. An error should only be returned in the rare case that
// Stop is called immediately after the command ends but before Start can
// update its internal state.
func (c *Cmd) Stop() error {
	c.Lock()
	defer c.Unlock()

	// Nothing to stop if Start hasn't been called, or the proc hasn't started,
	// or it's already done.
	if c.doneChan == nil || !c.started || c.done {
		return nil
	}

	// Flag that command was stopped, it didn't complete. This results in
	// status.Complete = false
	c.stopped = true

	// Signal the process group (-pid), not just the process, so that the process
	// and all its children are signaled. Else, child procs can keep running and
	// keep the stdout/stderr fd open and cause cmd.Wait to hang.
	return syscall.Kill(-c.status.PID, syscall.SIGTERM)
}

// Status returns the Status of the command at any time. It is safe to call
// concurrently by multiple goroutines.
func (c *Cmd) Status() Status {
	c.Lock()
	defer c.Unlock()

	// Return default status if cmd hasn't been started
	if c.doneChan == nil || !c.started {
		return c.status
	}

	if c.done {
		// No longer running
		if !c.final {
			c.status.Stdout = c.stdout.Lines()
			c.status.Stderr = c.stderr.Lines()

			c.stdout = nil // release buffers
			c.stderr = nil

			c.final = true
		}
	} else {
		// Still running
		c.status.Runtime = time.Now().Sub(c.startTime).Seconds()
		c.status.Stdout = c.stdout.Lines()
		c.status.Stderr = c.stderr.Lines()
	}

	return c.status
}

// --------------------------------------------------------------------------

func (c *Cmd) run() {
	defer func() {
		c.doneChan <- c.Status() // unblocks Start if caller is waiting
	}()

	// //////////////////////////////////////////////////////////////////////
	// Setup command
	// //////////////////////////////////////////////////////////////////////
	cmd := exec.Command(c.Name, c.Args...)

	// Set process group ID so the cmd and all its children become a new
	// process grouc. This allows Stop to SIGTERM thei cmd's process group
	// without killing this process (i.e. this code here).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Write stdout and stderr to buffers that are safe to read while writing
	// and don't cause a race condition.
	c.stdout = newOutput()
	c.stderr = newOutput()
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr

	// //////////////////////////////////////////////////////////////////////
	// Start command
	// //////////////////////////////////////////////////////////////////////
	now := time.Now()
	if err := cmd.Start(); err != nil {
		c.Lock()
		c.status.Error = err
		c.status.StartTs = now.UnixNano()
		c.status.StopTs = time.Now().UnixNano()
		c.done = true
		c.Unlock()
		return
	}

	// Set initial status
	c.Lock()
	c.startTime = now              // command is running
	c.status.PID = cmd.Process.Pid // command is running
	c.status.StartTs = now.UnixNano()
	c.started = true
	c.Unlock()

	// //////////////////////////////////////////////////////////////////////
	// Wait for command to finish or be killed
	// //////////////////////////////////////////////////////////////////////
	err := cmd.Wait()

	// Get exit code of the command
	exitCode := 0
	signaled := false
	if err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			err = nil // exec.ExitError isn't a standard error

			if waitStatus, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				exitCode = waitStatus.ExitStatus() // -1 if signaled

				// If the command was terminated by a signal, then exiterr.Error()
				// is a string like "signal: terminated".
				if waitStatus.Signaled() {
					signaled = true
					err = errors.New(exiterr.Error())
				}
			}
		}
	}

	// Set final status
	c.Lock()
	if !c.stopped && !signaled {
		c.status.Complete = true
	}
	c.status.Runtime = time.Now().Sub(c.startTime).Seconds()
	c.status.StopTs = time.Now().UnixNano()
	c.status.Exit = exitCode
	c.status.Error = err
	c.done = true
	c.Unlock()
}

// --------------------------------------------------------------------------

// os/exec.Cmd.StdoutPipe is usually used incorrectly. The docs are clear:
// "it is incorrect to call Wait before all reads from the pipe have completed."
// Therefore, we can't read from the pipe in another goroutine because it
// causes a race condition: we'll read in one goroutine and the original
// goroutine that calls Wait will write on close which is what Wait does.
// The proper solution is using an io.Writer for cmd.Stdout. I couldn't find
// an io.Writer that's also safe for concurrent reads (as lines in a []string
// no less), so I created one:
type output struct {
	buf   *bytes.Buffer
	lines []string
	*sync.Mutex
}

func newOutput() *output {
	return &output{
		buf:   &bytes.Buffer{},
		lines: []string{},
		Mutex: &sync.Mutex{},
	}
}

// io.Writer interface is only this method
func (rw *output) Write(p []byte) (int, error) {
	rw.Lock()
	defer rw.Unlock()
	return rw.buf.Write(p) // and bytes.Buffer implements it, too
}

func (rw *output) Lines() []string {
	rw.Lock()
	defer rw.Unlock()
	// Scanners are io.Readers which effectively destroy the buffer by reading
	// to EOF. So once we scan the buf to lines, the buf is empty again.
	s := bufio.NewScanner(rw.buf)
	for s.Scan() {
		rw.lines = append(rw.lines, s.Text())
	}
	return rw.lines
}
