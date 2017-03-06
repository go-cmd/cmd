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

	// Start process in bg and get chan to recieve final Status when done
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
		t.Fatal("got PID %d, expected PID > 0", s.PID)
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
