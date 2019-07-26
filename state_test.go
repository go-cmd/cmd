package cmd_test

import (
	"testing"

	"github.com/go-cmd/cmd"
)

func TestState1(t *testing.T) {
	var s cmd.CmdState

	s = cmd.INITIAL
	if s.String() != "initial" {
		t.Errorf("got State %s, expecting %s", s.String(), "initial")
	}

	s = cmd.STARTING
	if s.String() != "starting" {
		t.Errorf("got State %s, expecting %s", s.String(), "starting")
	}

	s = cmd.RUNNING
	if s.String() != "running" {
		t.Errorf("got State %s, expecting %s", s.String(), "running")
	}

	s = cmd.STOPPING
	if s.String() != "stopping" {
		t.Errorf("got State %s, expecting %s", s.String(), "stopping")
	}

	s = cmd.INTERRUPT
	if s.String() != "interrupted" {
		t.Errorf("got State %s, expecting %s", s.String(), "interrupted")
	}

	s = cmd.FINISHED
	if s.String() != "finished" {
		t.Errorf("got State %s, expecting %s", s.String(), "finished")
	}

	s = cmd.FATAL
	if s.String() != "fatal" {
		t.Errorf("got State %s, expecting %s", s.String(), "fatal")
	}
}

func TestState2(t *testing.T) {
	var s cmd.CmdState

	s = 0
	if s.String() != "initial" {
		t.Errorf("got State %s, expecting %s", s.String(), "initial")
	}

	s = 99
	if s.String() != "unknown" {
		t.Errorf("got State %s, expecting %s", s.String(), "unknown")
	}
}
