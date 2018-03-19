package cmd_test

import (
	"errors"
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

func TestStreamingOutput(t *testing.T) {
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
	p := cmd.NewCmdOutput(cmd.Output{Buffered: true, Streaming: true}, "./test/stream", tmpfile.Name())
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

	// Stop test/stream
	touchFile(tmpfile.Name())

	s := p.Status()
	if s.Exit != 0 {
		t.Error("got exit %d, expected 0", s.Exit)
	}

	// Kill the process
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}
