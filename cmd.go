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
	Name   string
	Args   []string
	Stdout chan string // streaming STDOUT if enabled, else nil (see Output)
	Stderr chan string // streaming STDERR if enabled, else nil (see Output)
	*sync.Mutex
	started   bool      // cmd.Start called, no error
	stopped   bool      // Stop called
	done      bool      // run() done
	final     bool      // status finalized in Status
	startTime time.Time // if started true
	stdout    *output   // low-level stdout buffering and streaming
	stderr    *output   // low-level stderr buffering and streaming
	status    Status
	doneChan  chan Status // nil until Start() called
	buffered  bool        // buffer STDOUT and STDERR to Status.Stdout and Std
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
	Complete bool     // false if stopped or signaled
	Exit     int      // exit code of process
	Error    error    // Go error
	StartTs  int64    // Unix ts (nanoseconds)
	StopTs   int64    // Unix ts (nanoseconds)
	Runtime  float64  // seconds
	Stdout   []string // buffered STDOUT
	Stderr   []string // buffered STDERR
}

// NewCmd creates a new Cmd for the given command name and arguments. The command
// is not started until Start is called. Output buffering is on, streaming output
// is off. To control output, see NewCmdOutput.
func NewCmd(name string, args ...string) *Cmd {
	return &Cmd{
		Name:     name,
		Args:     args,
		buffered: true,
		Mutex:    &sync.Mutex{},
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

// Output represents output control for NewCmdOutput. If Buffered is true,
// STDOUT and STDERR are written to Status.Stdout and Status.Stderr. The caller
// can call Cmd.Status to read output at intervals. If Streaming is true,
// Cmd.Stdout and Cmd.Stderr channels are created and STDOUT and STDERR output
// lines are written them in real time. This is faster and more efficient than
// polling Cmd.Status. The caller must read both streaming channels, else output
// lines are dropped silently.
type Output struct {
	Buffered  bool // STDOUT/STDERR to Status.Stdout/Status.Stderr
	Streaming bool // STDOUT/STDERR to Cmd.Stdout/Cmd.Stderr
}

// NewCmdOutput creates a new Cmd with custom output control.
func NewCmdOutput(output Output, name string, args ...string) *Cmd {
	out := NewCmd(name, args...)
	out.buffered = output.Buffered
	if output.Streaming {
		out.Stdout = make(chan string, 100)
		out.Stderr = make(chan string, 100)
	}
	return out
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
	c.stdout = newOutput(c.buffered, c.Stdout)
	c.stderr = newOutput(c.buffered, c.Stderr)
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
	buffered bool
	buf      *bytes.Buffer
	lines    []string
	*sync.Mutex

	streamChan chan string
	streamBuf  []byte
	n          int
	nextLine   int
}

func newOutput(buffered bool, streamChan chan string) *output {
	out := &output{
		buffered:   buffered,
		streamChan: streamChan,
	}
	if buffered {
		out.buf = &bytes.Buffer{}
		out.lines = []string{}
		out.Mutex = &sync.Mutex{}
	}
	if streamChan != nil {
		out.streamBuf = make([]byte, 16384)
		out.n = 0
		out.nextLine = 0
	}
	return out
}

// io.Writer interface is only this method
func (rw *output) Write(p []byte) (n int, err error) {
	if rw.buffered {
		rw.Lock()
		n, err = rw.buf.Write(p) // and bytes.Buffer implements it, too
		rw.Unlock()
	}

	if rw.streamChan == nil { // not streaming
		return // implicit
	}

	// Streaming
	copy(rw.streamBuf[rw.n:], p)
	rw.n += len(p)
	for {
		i := bytes.IndexByte(rw.streamBuf[rw.nextLine:], '\n')
		if i < 0 {
			break // no newline in stream, next line incomplete
		}
		eol := rw.nextLine + i // "line\n"
		if rw.streamBuf[i-1] == '\r' {
			eol -= 1 // "line\r\n"
		}
		select {
		case rw.streamChan <- string(rw.streamBuf[rw.nextLine:eol]):
		default:
		}
		rw.nextLine += i + 1
		if rw.nextLine == rw.n {
			rw.n = 0
			rw.nextLine = 0
			break // end of stream
		}
	}
	if !rw.buffered {
		n = len(p)
	}

	return // implicit
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
