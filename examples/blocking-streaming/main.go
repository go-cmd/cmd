package main

import (
	"fmt"
	"time"

	"github.com/go-cmd/cmd"
)

func main() {
	// Disable output buffering, enable streaming
	cmdOptions := cmd.Options{
		Buffered:  false,
		Streaming: true,
	}

	// Create Cmd with options
	envCmd := cmd.NewCmdOptions(cmdOptions, "env")

	// Print STDOUT lines streaming from Cmd
	go func() {
		for line := range envCmd.Stdout {
			fmt.Println(line)
		}
	}()

	// Run and wait for Cmd to return, discard Status
	<-envCmd.Start()

	// Cmd has finished but wait for goroutine to print all lines
	for len(envCmd.Stdout) > 0 {
		time.Sleep(10 * time.Millisecond)
	}
}
