package cmd_test

import (
	"errors"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/go-cmd/cmd"
	"github.com/go-test/deep"
)

func TestCmdOK(t *testing.T) {
	now := time.Now().Unix()

	p := cmd.NewCmd("echo", "foo")
	gotStatus := <-p.Start()
	expectStatus := cmd.Status{
		Cmd:      "echo",
		PID:      gotStatus.PID, // nondeterministic
		Complete: true,
		Exit:     0,
		Error:    nil,
		Runtime:  gotStatus.Runtime, // nondeterministic
		Stdout:   []string{"foo"},
		Stderr:   []string{},
	}
	if gotStatus.StartTs < now {
		t.Error("StartTs < now")
	}
	if gotStatus.StopTs < gotStatus.StartTs {
		t.Error("StopTs < StartTs")
	}
	gotStatus.StartTs = 0
	gotStatus.StopTs = 0
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Error(diffs)
	}
	if gotStatus.PID < 0 {
		t.Errorf("got PID %d, expected non-zero", gotStatus.PID)
	}
	if gotStatus.Runtime < 0 {
		t.Errorf("got runtime %f, expected non-zero", gotStatus.Runtime)
	}
}

func TestCmdNonzeroExit(t *testing.T) {
	p := cmd.NewCmd("false")
	gotStatus := <-p.Start()
	expectStatus := cmd.Status{
		Cmd:      "false",
		PID:      gotStatus.PID, // nondeterministic
		Complete: true,
		Exit:     1,
		Error:    nil,
		Runtime:  gotStatus.Runtime, // nondeterministic
		Stdout:   []string{},
		Stderr:   []string{},
	}
	gotStatus.StartTs = 0
	gotStatus.StopTs = 0
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Error(diffs)
	}
	if gotStatus.PID < 0 {
		t.Errorf("got PID %d, expected non-zero", gotStatus.PID)
	}
	if gotStatus.Runtime < 0 {
		t.Errorf("got runtime %f, expected non-zero", gotStatus.Runtime)
	}
}

func TestCmdStop(t *testing.T) {
	// Count to 3 sleeping 5s between counts. The long sleep is because we want
	// to kill the proc right after count "1" to ensure Stdout only contains "1"
	// and also to ensure that the proc is really killed instantly because if
	// it's not then timeout below will trigger.
	p := cmd.NewCmd("./test/count-and-sleep", "3", "5")

	// Start process in bg and get chan to receive final Status when done
	statusChan := p.Start()

	// Give it a second
	time.Sleep(1 * time.Second)

	// Kill the process
	err := p.Stop()
	if err != nil {
		t.Error(err)
	}

	// The final status should be returned instantly
	timeout := time.After(1 * time.Second)
	var gotStatus cmd.Status
	select {
	case gotStatus = <-statusChan:
	case <-timeout:
		t.Fatal("timeout waiting for statusChan")
	}

	start := time.Unix(0, gotStatus.StartTs)
	stop := time.Unix(0, gotStatus.StopTs)
	d := stop.Sub(start).Seconds()
	if d < 0.90 || d > 2 {
		t.Errorf("stop - start time not between 0.9s and 2.0s: %s - %s = %f", stop, start, d)
	}
	gotStatus.StartTs = 0
	gotStatus.StopTs = 0

	expectStatus := cmd.Status{
		Cmd:      "./test/count-and-sleep",
		PID:      gotStatus.PID,                    // nondeterministic
		Complete: false,                            // signaled by Stop
		Exit:     -1,                               // signaled by Stop
		Error:    errors.New("signal: terminated"), // signaled by Stop
		Runtime:  gotStatus.Runtime,                // nondeterministic
		Stdout:   []string{"1"},
		Stderr:   []string{},
	}
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Error(diffs)
	}
	if gotStatus.PID < 0 {
		t.Errorf("got PID %d, expected non-zero", gotStatus.PID)
	}
	if gotStatus.Runtime < 0 {
		t.Errorf("got runtime %f, expected non-zero", gotStatus.Runtime)
	}

	// Stop should be idempotent
	err = p.Stop()
	if err != nil {
		t.Error(err)
	}

	// Start should be idempotent, too. It just returns the same statusChan again.
	c2 := p.Start()
	if diffs := deep.Equal(statusChan, c2); diffs != nil {
		t.Error(diffs)
	}
}

func TestCmdNotStarted(t *testing.T) {
	// Call everything _but_ Start.
	p := cmd.NewCmd("echo", "foo")

	gotStatus := p.Status()
	expectStatus := cmd.Status{
		Cmd:      "echo",
		PID:      0,
		Complete: false,
		Exit:     -1,
		Error:    nil,
		Runtime:  0,
		Stdout:   nil,
		Stderr:   nil,
	}
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Error(diffs)
	}

	err := p.Stop()
	if err != nil {
		t.Error(err)
	}
}

func TestCmdOutput(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "cmd.TestCmdOutput")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	p := cmd.NewCmd("./test/touch-file-count", tmpfile.Name())

	p.Start()

	touchFile := func(file string) {
		if err := exec.Command("touch", file).Run(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(700 * time.Millisecond)
	}
	var s cmd.Status
	var stdout []string

	touchFile(tmpfile.Name())
	s = p.Status()
	stdout = []string{"1"}
	if diffs := deep.Equal(s.Stdout, stdout); diffs != nil {
		t.Log(s.Stdout)
		t.Error(diffs)
	}

	touchFile(tmpfile.Name())
	s = p.Status()
	stdout = []string{"1", "2"}
	if diffs := deep.Equal(s.Stdout, stdout); diffs != nil {
		t.Log(s.Stdout)
		t.Error(diffs)
	}

	// No more output yet
	s = p.Status()
	stdout = []string{"1", "2"}
	if diffs := deep.Equal(s.Stdout, stdout); diffs != nil {
		t.Log(s.Stdout)
		t.Error(diffs)
	}

	// +2 lines
	touchFile(tmpfile.Name())
	touchFile(tmpfile.Name())
	s = p.Status()
	stdout = []string{"1", "2", "3", "4"}
	if diffs := deep.Equal(s.Stdout, stdout); diffs != nil {
		t.Log(s.Stdout)
		t.Error(diffs)
	}

	// Kill the process
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}

func TestCmdNotFound(t *testing.T) {
	p := cmd.NewCmd("cmd-does-not-exist")
	gotStatus := <-p.Start()
	gotStatus.StartTs = 0
	gotStatus.StopTs = 0
	expectStatus := cmd.Status{
		Cmd:      "cmd-does-not-exist",
		PID:      0,
		Complete: false,
		Exit:     -1,
		Error:    errors.New(`exec: "cmd-does-not-exist": executable file not found in $PATH`),
		Runtime:  0,
		Stdout:   nil,
		Stderr:   nil,
	}
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Logf("%+v", gotStatus)
		t.Error(diffs)
	}
}

func TestCmdLost(t *testing.T) {
	// Test something like the kernel OOM killing the proc. So the proc is
	// stopped outside our control.
	p := cmd.NewCmd("./test/count-and-sleep", "3", "5")

	statusChan := p.Start()

	// Give it a second
	time.Sleep(1 * time.Second)

	// Get the PID and kill it
	s := p.Status()
	if s.PID <= 0 {
		t.Fatalf("got PID %d, expected PID > 0", s.PID)
	}
	pgid, err := syscall.Getpgid(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	syscall.Kill(-pgid, syscall.SIGKILL) // -pid = process group of pid

	// Even though killed externally, our wait should return instantly
	timeout := time.After(1 * time.Second)
	var gotStatus cmd.Status
	select {
	case gotStatus = <-statusChan:
	case <-timeout:
		t.Fatal("timeout waiting for statusChan")
	}
	gotStatus.Runtime = 0 // nondeterministic
	gotStatus.StartTs = 0
	gotStatus.StopTs = 0

	expectStatus := cmd.Status{
		Cmd:      "./test/count-and-sleep",
		PID:      s.PID,
		Complete: false,
		Exit:     -1,
		Error:    errors.New("signal: killed"),
		Runtime:  0,
		Stdout:   []string{"1"},
		Stderr:   []string{},
	}
	if diffs := deep.Equal(gotStatus, expectStatus); diffs != nil {
		t.Logf("%+v\n", gotStatus)
		t.Error(diffs)
	}
}

func TestCmdBothOutput(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "cmd.TestStreamingOutput")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tmpfile.Name()); err != nil {
		t.Fatal(err)
	}

	touchFile := func(file string) {
		if err := exec.Command("touch", file).Run(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Streams a count to stdout and stderr until given file exists
	// Output like:
	//   stdout 1
	//   stderr 1
	//   stdout 2
	//   stderr 2
	// Where each is printed on stdout and stderr as indicated.
	p := cmd.NewCmdOptions(cmd.Options{Buffered: true, Streaming: true}, "./test/stream", tmpfile.Name())
	p.Start()
	time.Sleep(250 * time.Millisecond) // give test/stream a moment to print something

	timeout := time.After(10 * time.Second) // test timeout

	// test/stream is spewing output, so we should be able to read it while
	// the cmd is running. Try and fetch 3 lines from stdout and stderr.
	i := 0
	stdoutPrevLine := ""
	stderrPrevLine := ""
	readLines := 3
	lines := 0
	for i < readLines {
		i++

		// STDOUT
		select {
		case curLine := <-p.Stdout:
			t.Logf("got line: '%s'", curLine)
			if curLine == "" {
				// Shouldn't happen because test/stream doesn't print empty lines.
				// This indicates a bug in the stream buffer handling.
				t.Fatal("got empty line")
			}
			if stdoutPrevLine != "" && curLine == stdoutPrevLine {
				t.Fatal("current line == previous line, expected new output:\ncprev: %s\ncur: %s\n", stdoutPrevLine, curLine)
			}
			stdoutPrevLine = curLine
			lines++
		case <-timeout:
			t.Fatal("timeout reading streaming output")
		default:
		}

		// STDERR
		select {
		case curLine := <-p.Stderr:
			t.Logf("got line: '%s'", curLine)
			if curLine == "" {
				// Shouldn't happen because test/stream doesn't print empty lines.
				// This indicates a bug in the stream buffer handling.
				t.Fatal("got empty line")
			}
			if stderrPrevLine != "" && curLine == stderrPrevLine {
				t.Fatal("current line == previous line, expected new output:\ncprev: %s\ncur: %s\n", stderrPrevLine, curLine)
			}
			stderrPrevLine = curLine
			lines++
		case <-timeout:
			t.Fatal("timeout reading streaming output")
		default:
		}

		time.Sleep(200 * time.Millisecond)
	}

	// readLines * 2 (stdout and stderr)
	if lines != readLines*2 {
		t.Fatalf("read %d lines from streaming output, expected 6", lines)
	}

	s := p.Status()
	if len(s.Stdout) < readLines {
		t.Fatalf("read %d lines from buffered STDOUT, expected %d", len(s.Stdout), readLines)
	}
	if len(s.Stderr) < readLines {
		t.Fatalf("read %d lines from buffered STDERR, expected %d", len(s.Stderr), readLines)
	}

	// Stop test/stream
	touchFile(tmpfile.Name())

	s = p.Status()
	if s.Exit != 0 {
		t.Error("got exit %d, expected 0", s.Exit)
	}

	// Kill the process
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}

func TestCmdOnlyStreamingOutput(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "cmd.TestStreamingOutput")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tmpfile.Name()); err != nil {
		t.Fatal(err)
	}

	touchFile := func(file string) {
		if err := exec.Command("touch", file).Run(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Streams a count to stdout and stderr until given file exists
	// Output like:
	//   stdout 1
	//   stderr 1
	//   stdout 2
	//   stderr 2
	// Where each is printed on stdout and stderr as indicated.
	p := cmd.NewCmdOptions(cmd.Options{Buffered: false, Streaming: true}, "./test/stream", tmpfile.Name())
	p.Start()
	time.Sleep(250 * time.Millisecond) // give test/stream a moment to print something

	timeout := time.After(10 * time.Second) // test timeout

	// test/stream is spewing output, so we should be able to read it while
	// the cmd is running. Try and fetch 3 lines from stdout and stderr.
	i := 0
	stdoutPrevLine := ""
	stderrPrevLine := ""
	readLines := 3
	lines := 0
	for i < readLines {
		i++

		// STDOUT
		select {
		case curLine := <-p.Stdout:
			t.Logf("got line: '%s'", curLine)
			if curLine == "" {
				// Shouldn't happen because test/stream doesn't print empty lines.
				// This indicates a bug in the stream buffer handling.
				t.Fatal("got empty line")
			}
			if stdoutPrevLine != "" && curLine == stdoutPrevLine {
				t.Fatal("current line == previous line, expected new output:\ncprev: %s\ncur: %s\n", stdoutPrevLine, curLine)
			}
			stdoutPrevLine = curLine
			lines++
		case <-timeout:
			t.Fatal("timeout reading streaming output")
		default:
		}

		// STDERR
		select {
		case curLine := <-p.Stderr:
			t.Logf("got line: '%s'", curLine)
			if curLine == "" {
				// Shouldn't happen because test/stream doesn't print empty lines.
				// This indicates a bug in the stream buffer handling.
				t.Fatal("got empty line")
			}
			if stderrPrevLine != "" && curLine == stderrPrevLine {
				t.Fatal("current line == previous line, expected new output:\ncprev: %s\ncur: %s\n", stderrPrevLine, curLine)
			}
			stderrPrevLine = curLine
			lines++
		case <-timeout:
			t.Fatal("timeout reading streaming output")
		default:
		}

		time.Sleep(200 * time.Millisecond)
	}

	// readLines * 2 (stdout and stderr)
	if lines != readLines*2 {
		t.Fatalf("read %d lines from streaming output, expected 6", lines)
	}

	s := p.Status()
	if len(s.Stdout) != 0 {
		t.Fatalf("read %d lines from buffered STDOUT, expected 0", len(s.Stdout))
	}
	if len(s.Stderr) != 0 {
		t.Fatalf("read %d lines from buffered STDERR, expected 0", len(s.Stderr))
	}

	// Stop test/stream
	touchFile(tmpfile.Name())

	s = p.Status()
	if s.Exit != 0 {
		t.Error("got exit %d, expected 0", s.Exit)
	}

	// Kill the process
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}

func TestStreamingOverflow(t *testing.T) {
	// Make a line that will fill up and overflow the steaming buffer by 2 chars:
	// "bc", plus newline. The line will be truncated at "bc\n" so we only get back
	// the "aaa.." long string.
	longLine := make([]byte, cmd.STREAM_BUFFER_SIZE+3) // "a...bc\n"
	for i := 0; i < cmd.STREAM_BUFFER_SIZE; i++ {
		longLine[i] = 'a'
	}
	longLine[cmd.STREAM_BUFFER_SIZE] = 'b'
	longLine[cmd.STREAM_BUFFER_SIZE+1] = 'c'
	longLine[cmd.STREAM_BUFFER_SIZE+2] = '\n'

	// Make new streaming output on our lines chan
	lines := make(chan string, 5)
	out := cmd.NewOutputStream(lines)

	// Write the long line, it should only write (n) up to cmd.STREAM_BUFFER_SIZE
	n, err := out.Write(longLine)
	if n != cmd.STREAM_BUFFER_SIZE {
		t.Error("Write n = %d, expected %d", n, cmd.STREAM_BUFFER_SIZE)
	}
	if err != io.ErrShortWrite {
		t.Errorf("got err '%v', expected io.ErrShortWrite")
	}

	// Get first, truncated line
	var gotLine string
	select {
	case gotLine = <-lines:
	default:
		t.Fatal("blocked on <-lines")
	}

	// Up to but not include "bc\n" because it should have been truncated
	if gotLine != string(longLine[0:cmd.STREAM_BUFFER_SIZE]) {
		t.Logf("got line: '%s'", gotLine)
		t.Error("did not get expected first line (see log above), expected only \"aaa...\" part")
	}

	// Streaming should still work as normal after an overflow; send it a line
	n, err = out.Write([]byte("foo\n"))
	if n != 4 {
		t.Errorf("got n %d, expected 4", n)
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil", err)
	}

	select {
	case gotLine = <-lines:
	default:
		t.Fatal("blocked on <-lines")
	}

	if gotLine != "foo" {
		t.Errorf("got line: '%s', expected 'foo'", gotLine)
	}
}

func TestStreamingMultipleLines(t *testing.T) {
	lines := make(chan string, 5)
	out := cmd.NewOutputStream(lines)

	// Quick side test: Lines() chan string should be the same chan string
	// we created the object with
	if out.Lines() != lines {
		t.Errorf("Lines() does not return the given string chan")
	}

	// Write two short lines
	input := "foo\nbar\n"
	n, err := out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}

	// Get one line
	var gotLine string
	select {
	case gotLine = <-lines:
	default:
		t.Fatal("blocked on <-lines")
	}

	// "foo" should be sent before "bar" because that was the input
	if gotLine != "foo" {
		t.Errorf("got line: '%s', expected 'foo'", gotLine)
	}

	// Get next line
	select {
	case gotLine = <-lines:
	default:
		t.Fatal("blocked on <-lines")
	}

	if gotLine != "bar" {
		t.Errorf("got line: '%s', expected 'bar'", gotLine)
	}
}

func TestStreamingBlankLines(t *testing.T) {
	lines := make(chan string, 5)
	out := cmd.NewOutputStream(lines)

	// Blank line in the middle
	input := "foo\n\nbar\n"
	expectLines := []string{"foo", "", "bar"}
	gotLines := []string{}
	n, err := out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES1:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES1
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}

	// All blank lines
	input = "\n\n\n"
	expectLines = []string{"", "", ""}
	gotLines = []string{}
	n, err = out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES2:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES2
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}

	// Blank lines at end
	input = "foo\n\n\n"
	expectLines = []string{"foo", "", ""}
	gotLines = []string{}
	n, err = out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES3:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES3
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}
}

func TestStreamingCarriageReturn(t *testing.T) {
	lines := make(chan string, 5)
	out := cmd.NewOutputStream(lines)

	input := "foo\r\nbar\r\n"
	expectLines := []string{"foo", "bar"}
	gotLines := []string{}
	n, err := out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES1:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES1
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}
}

func TestStreamingDropsLines(t *testing.T) {
	lines := make(chan string, 3)
	out := cmd.NewOutputStream(lines)

	// Fill up the chan so Write blocks. We'll receive these instead of...
	lines <- "1"
	lines <- "2"
	lines <- "3"

	// ...new lines that we shouldn't receive because "123" is already in the chan.
	input := "A\nB\nC\n"
	expectLines := []string{"1", "2", "3"}
	gotLines := []string{}
	n, err := out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES1:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES1
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}

	// Now that chan is clear, we should receive only new lines.
	input = "D\nE\nF\n"
	expectLines = []string{"D", "E", "F"}
	gotLines = []string{}
	n, err = out.Write([]byte(input))
	if n != len(input) {
		t.Error("Write n = %d, expected %d", n, len(input))
	}
	if err != nil {
		t.Errorf("got err '%v', expected nil")
	}
LINES2:
	for {
		select {
		case line := <-lines:
			gotLines = append(gotLines, line)
		default:
			break LINES2
		}
	}
	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}

	if diffs := deep.Equal(gotLines, expectLines); diffs != nil {
		t.Error(diffs)
	}
}
