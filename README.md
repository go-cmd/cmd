# go-cmd/Cmd

[![Go Report Card](https://goreportcard.com/badge/github.com/go-cmd/cmd)](https://goreportcard.com/report/github.com/go-cmd/cmd) [![Build Status](https://travis-ci.org/go-cmd/cmd.svg?branch=master)](https://travis-ci.org/go-cmd/cmd) [![GoDoc](https://godoc.org/github.com/go-cmd/cmd?status.svg)](https://godoc.org/github.com/go-cmd/cmd)

This package is a small but very useful wrapper around [os/exec.Cmd](https://golang.org/pkg/os/exec/#Cmd) that makes it dead simple and safe to run commands in asynchronous, highly concurrent, real-time applications. Here's the look and feel:

```go
import "github.com/go-cmd/cmd"

// Start a long-running process, capture stdout and stderr
c := cmd.NewCmd("find", "/", "--name" "needle")
statusChan := c.Start()

// Print last line of stdout every 2s
go func() {
  for range time.Ticker(2 * time.Second).C {
    status := c.Status()
    n := len(status.Stdout)
    fmt.Println(status.Stdout[n - 1])
  }
}()

// Stop command after 1 hour
go func() {
  <-time.After(1 * time.Hour)
  c.Stop()
}()

// Check if command is done
switch {
case finalStatus := <-statusChan:
  // yes!
default:
  // no, still running
}

// Block waiting for command to exit, be stopped, or be killed
finalStatus := <-statusChan
```

That's it, only three methods: `Start`, `Stop`, and `Status`. Although free, here are the selling points of `go-cmd/Cmd`:

1. Channel-based fire and forget
1. Real-time stdout and stderr
1. Real-time status
1. Complete and consolidated return
1. Proper process termination
1. _100%_ test coverage, no race conditions

### Channel-based fire and forget

As the example above shows, starting a command immediately returns a channel to which the final status is sent when the command exits for any reason. So by default commands run asynchronously, but running synchronously is possible and easy, too:

```go
// Run foo and block waiting for it to exit
c := cmd.NewCmd("foo")
s := <-c.Start()
```
To achieve similar with  Go built-in `Cmd` requires everything this package already does.

### Real-time stdout and stderr

It's common to want to read stdout or stderr _while_ the command is running. The common approach is to call [StdoutPipe](https://golang.org/pkg/os/exec/#Cmd.StdoutPipe) and read from the provided `io.ReadCloser`. This works but it causes a race condition (that `go test -race` detects) and the docs say not to do it: "it is incorrect to call Wait before all reads from the pipe have completed".

The proper solution is to set the `io.Writer` of `Stdout`. To be thread-safe and non-racey, this requires further work to write while possibly N-many goroutines read. `go-cmd/Cmd` has already done this work.

### Real-time status

Similar to real-time stdout and stderr, it's nice to see, for example, elapsed runtime. This package allows that: `Status` can be called any time by any goroutine, and it returns this struct:
```go
type Status struct {
    Cmd      string
    PID      int
    Complete bool
    Exit     int
    Error    error
    Runtime  float64 // seconds
    Stdout   []string
    Stderr   []string
}
```

### Complete and consolidated return

Speaking of that struct above, Go built-in `Cmd` does not put all the return information in one place, which is fine because Go is awesome! But to save some time, `go-cmd/Cmd` uses the `Status` struct above to convey all information about the command. Even when the command finishes, calling `Status` returns the final status, the same final status sent to the status channel returned by the call to `Start`.

### Proper process termination

It's been said that everyone love's a good mystery. Then here's one: _process group ID_. If you know, then wow, congratulations! If not, don't feel bad. It took me hours one Saturday evening to solve this mystery. Let's just say that Go built-in [Wait](https://golang.org/pkg/os/exec/#Cmd.Wait) can still block even after the command is killed. But not `go-cmd/Cmd`. You can rely on `Stop` in this package.

### 100% test coverage, no race conditions

Enough said.

---

## Acknowledgements

[Brian Ip](https://github.com/BrianIp) wrote the original code to get the exit status. Strangely, Go doesn't just provide this, it requires magic like `exiterr.Sys().(syscall.WaitStatus)` and more.
