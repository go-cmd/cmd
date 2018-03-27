// Package cmd runs external commands with concurrent access to output and status.
// It wraps the Go standard library os/exec.Command to correctly handle reading
// output (STDOUT and STDERR) while a command is running and killing a command.
// All operations are safe to call from multiple goroutines.
//
// A simple example that runs env and prints its output:
//
//   import (
//       "fmt"
//       "github.com/go-cmd/cmd"
//   )
//
//   func main() {
//       // Create Cmd, buffered output
//       envCmd := cmd.NewCmd("env")
//
//       // Run and wait for Cmd to return Status
//       status := <-envCmd.Start()
//
//       // Print each line of STDOUT from Cmd
//       for _, line := range status.Stdout {
//           fmt.Println(line)
//       }
//   }
//
// Commands can be ran synchronously (blocking) or asynchronously (non-blocking):
//
//   envCmd := cmd.NewCmd("env") // create
//
//   status := <-envCmd.Start() // run blocking
//
//   statusChan := envCmd.Start() // run non-blocking
//   // Do other work while Cmd is running...
//   status <- statusChan // blocking
//
// Start returns a channel, so the first example is blocking because it receives
// on the channel, but the second example is non-blocking because it saves the
// channel and receives on it later. The Status function can be called while the
// Cmd is running. When the Cmd finishes, a final status is sent to the channel
// returned by Start.
package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Cmd represents an external command, similar to the Go built-in os/exec.Cmd.
// A Cmd cannot be reused after calling Start. Do not modify exported fields;
// they are read-only. To create a new Cmd, call NewCmd or NewCmdOptions.
type Cmd struct {
	Name   string
	Args   []string
	Stdout chan string // streaming STDOUT if enabled, else nil (see Options)
	Stderr chan string // streaming STDERR if enabled, else nil (see Options)
	*sync.Mutex
	started   bool          // cmd.Start called, no error
	stopped   bool          // Stop called
	done      bool          // run() done
	final     bool          // status finalized in Status
	startTime time.Time     // if started true
	stdout    *OutputBuffer // low-level stdout buffering and streaming
	stderr    *OutputBuffer // low-level stderr buffering and streaming
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
// is off. To control output, use NewCmdOptions instead.
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

// Options represents customizations for NewCmdOptions.
type Options struct {
	// If Buffered is true, STDOUT and STDERR are written to Status.Stdout and
	// Status.Stderr. The caller can call Cmd.Status to read output at intervals.
	Buffered bool

	// If Streaming is true, Cmd.Stdout and Cmd.Stderr channels are created and
	// STDOUT and STDERR output lines are written them in real time. This is
	// faster and more efficient than polling Cmd.Status. The caller must read both
	// streaming channels, else lines are dropped silently.
	Streaming bool
}

// NewCmdOptions creates a new Cmd with options. The command is not started
// until Start is called.
func NewCmdOptions(options Options, name string, args ...string) *Cmd {
	out := NewCmd(name, args...)
	out.buffered = options.Buffered
	if options.Streaming {
		out.Stdout = make(chan string, STREAM_CHAN_SIZE)
		out.Stderr = make(chan string, STREAM_CHAN_SIZE)
	}
	return out
}

// Start starts the command and immediately returns a channel that the caller
// can use to receive the final Status of the command when it ends. The caller
// can start the command and wait like,
//
//   status := <-myCmd.Start() // blocking
//
// or start the command asynchronously and be notified later when it ends,
//
//   statusChan := myCmd.Start() // non-blocking
//   // Do other work while Cmd is running...
//   status := <-statusChan // blocking
//
// Exactly one Status is sent on the channel when the command ends. The channel
// is not closed. Any error is set to Status.Error. Start is idempotent; it always
// returns the same channel.
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
			if c.buffered {
				c.status.Stdout = c.stdout.Lines()
				c.status.Stderr = c.stderr.Lines()
				c.stdout = nil // release buffers
				c.stderr = nil
			}
			c.final = true
		}
	} else {
		// Still running
		c.status.Runtime = time.Now().Sub(c.startTime).Seconds()
		if c.buffered {
			c.status.Stdout = c.stdout.Lines()
			c.status.Stderr = c.stderr.Lines()
		}
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
	if c.buffered && c.Stdout != nil {
		// Buffered and streaming, create both and combine with io.MultiWriter
		c.stdout = NewOutputBuffer()
		c.stderr = NewOutputBuffer()
		cmd.Stdout = io.MultiWriter(NewOutputStream(c.Stdout), c.stdout)
		cmd.Stderr = io.MultiWriter(NewOutputStream(c.Stderr), c.stderr)
	} else if c.buffered {
		// Buffered only
		c.stdout = NewOutputBuffer()
		c.stderr = NewOutputBuffer()
		cmd.Stdout = c.stdout
		cmd.Stderr = c.stderr
	} else {
		// Streaming only
		cmd.Stdout = NewOutputStream(c.Stdout)
		cmd.Stderr = NewOutputStream(c.Stderr)
	}

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

// //////////////////////////////////////////////////////////////////////////
// Output
// //////////////////////////////////////////////////////////////////////////

// os/exec.Cmd.StdoutPipe is usually used incorrectly. The docs are clear:
// "it is incorrect to call Wait before all reads from the pipe have completed."
// Therefore, we can't read from the pipe in another goroutine because it
// causes a race condition: we'll read in one goroutine and the original
// goroutine that calls Wait will write on close which is what Wait does.
// The proper solution is using an io.Writer for cmd.Stdout. I couldn't find
// an io.Writer that's also safe for concurrent reads (as lines in a []string
// no less), so I created one:

// OutputBuffer represents command output that is saved, line by line, in an
// unbounded buffer. It is safe for multiple goroutines to read while the command
// is running and after it has finished. If output is small (a few megabytes)
// and not read frequently, an output buffer is a good solution.
//
// A Cmd in this package uses an OutputBuffer for both STDOUT and STDERR by
// default when created by calling NewCmd. To use OutputBuffer directly with
// a Go standard library os/exec.Command:
//
//   import "os/exec"
//   import "github.com/go-cmd/cmd"
//   runnableCmd := exec.Command(...)
//   stdout := cmd.NewOutputBuffer()
//   runnableCmd.Stdout = stdout
//
// While runnableCmd is running, call stdout.Lines() to read all output
// currently written.
type OutputBuffer struct {
	buf   *bytes.Buffer
	lines []string
	*sync.Mutex
}

// NewOutputBuffer creates a new output buffer. The buffer is unbounded and safe
// for multiple goroutines to read while the command is running by calling Lines.
func NewOutputBuffer() *OutputBuffer {
	out := &OutputBuffer{
		buf:   &bytes.Buffer{},
		lines: []string{},
		Mutex: &sync.Mutex{},
	}
	return out
}

// Write makes OutputBuffer implement the io.Writer interface. Do not call
// this function directly.
func (rw *OutputBuffer) Write(p []byte) (n int, err error) {
	rw.Lock()
	n, err = rw.buf.Write(p) // and bytes.Buffer implements io.Writer
	rw.Unlock()
	return // implicit
}

// Lines returns lines of output written by the Cmd. It is safe to call while
// the Cmd is running and after it has finished. Subsequent calls returns more
// lines, if more lines were written. "\r\n" are stripped from the lines.
func (rw *OutputBuffer) Lines() []string {
	rw.Lock()
	// Scanners are io.Readers which effectively destroy the buffer by reading
	// to EOF. So once we scan the buf to lines, the buf is empty again.
	s := bufio.NewScanner(rw.buf)
	for s.Scan() {
		rw.lines = append(rw.lines, s.Text())
	}
	rw.Unlock()
	return rw.lines
}

// --------------------------------------------------------------------------

// OutputStream represents real time, line by line output from a running Cmd.
// Lines are terminated by a single newline preceded by an optional carriage
// return. Both newline and carriage return are stripped from the line when
// sent to a caller-provided channel. The caller should begin receiving before
// starting the Cmd. If the channel blocks, lines are dropped. Lines are
// discarded after being sent. The channel is not closed by the OutputStream.
//
// If output is large or written very fast, this is a better solution than
// OutputBuffer because the internal stream buffer is bounded at STREAM_BUFFER_SIZE
// bytes. For multiple goroutines to read from the same channel, the caller must
// implement its own multiplexing solution.
//
// A Cmd in this package uses an OutputStream for both STDOUT and STDERR when
// created by calling NewCmdOptions and Options.Streaming is true. To use
// OutputStream directly with a Go standard library os/exec.Command:
//
//   import "os/exec"
//   import "github.com/go-cmd/cmd"
//
//   stdoutChan := make(chan string, 100)
//   go func() {
//     for line := range stdoutChan {
//       // Do something with the line
//     }
//   }()
//
//   runnableCmd := exec.Command(...)
//   stdout := cmd.NewOutputStream(stdoutChan)
//   runnableCmd.Stdout = stdout
//
// While runnableCmd is running, lines are sent to the channels as soon as they
// are written by the command. The channel is not closed by the OutputStream.
type OutputStream struct {
	streamChan chan string
	streamBuf  []byte
	n          int
	nextLine   int
}

const (
	// STREAM_BUFFER_SIZE is the size of the OutputStream internal buffer.
	// Lines are truncated at this length. For example, if a line is three
	// times this length, only the first third is sent.
	STREAM_BUFFER_SIZE = 8192

	// STREAM_CHAN_SIZE is the default string channel size for a Cmd when
	// Options.Streaming is true.
	STREAM_CHAN_SIZE = 100
)

// NewOutputStream creates a new streaming output on the given channel. The
// caller should begin receiving on the channel before the command is started.
// The OutputStream never closes the channel.
func NewOutputStream(streamChan chan string) *OutputStream {
	out := &OutputStream{
		streamChan: streamChan,
		streamBuf:  make([]byte, STREAM_BUFFER_SIZE),
		n:          0,
		nextLine:   0,
	}
	return out
}

// Write makes OutputStream implement the io.Writer interface. Do not call
// this function directly.
func (rw *OutputStream) Write(p []byte) (n int, err error) {
	// Determine how much of p we can save in our buffer
	total := len(p)
	free := len(rw.streamBuf[rw.n:])
	if rw.n+total <= free {
		// all of p
		copy(rw.streamBuf[rw.n:], p)
		rw.n += total
		n = total // on implicit return
	} else {
		// part of p
		copy(rw.streamBuf[rw.n:], p[0:free])
		rw.n += free
		n = free // on implicit return
	}

	// Send each line in streamBuf
LINES:
	for {
		// Find next newline in stream buffer. nextLine starts at 0, but buff
		// can containe multiple lines, like "foo\nbar". So in that case nextLine
		// will be 0 ("foo\nbar\n") then 4 ("bar\n") on next iteration. And i
		// will be 3 and 7, respectively. So lines are [0:3] are [4:7].
		i := bytes.IndexByte(rw.streamBuf[rw.nextLine:], '\n')
		if i < 0 {
			break LINES // no newline in stream, next line incomplete
		}

		// End of line offset is start (nextLine) + newline offset. Like bufio.Scanner,
		// we allow \r\n but strip the \r too by decrementing the offset for that byte.
		eol := rw.nextLine + i // "line\n"
		if i > 0 && rw.streamBuf[i-1] == '\r' {
			eol -= 1 // "line\r\n"
		}

		// Send the string
		select {
		case rw.streamChan <- string(rw.streamBuf[rw.nextLine:eol]):
		default:
			// Channel blocked, caller isn't listening? Silently drop the string.
		}

		// Next line offset is the first byte (+1) after the newline (i)
		rw.nextLine += i + 1

		// If next line offset is the end of the buffer (rw.n), then we've processed
		// all lines. Reset everything back to offset 0.
		if rw.nextLine == rw.n {
			rw.n = 0
			rw.nextLine = 0
			break LINES
		}

		// Next line offset is < end of buffer (rw.n), so keep processing lines
	}

	// nextLine is reset to zero when a string is complete (has a newline),
	// so if > 0 _and_ n is at max capacity, then buffer is full and there's
	// an incomplete string. We don't support double-buffering, so send the
	// partial string and reset. Note: if nextLine > 0 but n < buff size,
	// then it's an incomplete string but we have buffer space to wait for
	// the rest of it.
	if rw.n == STREAM_BUFFER_SIZE {
		select {
		case rw.streamChan <- string(rw.streamBuf[rw.nextLine:rw.n]):
		default:
		}
		rw.n = 0
		rw.nextLine = 0
		err = io.ErrShortWrite // on implicit return
	}

	return // implicit
}

// Lines returns the channel to which lines are sent. This is the same channel
// passed to NewOutputStream.
func (rw *OutputStream) Lines() <-chan string {
	return rw.streamChan
}
