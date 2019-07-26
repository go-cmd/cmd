package cmd

const (
	INITIAL = iota
	STARTING
	RUNNING
	STOPPING
	INTERRUPT // final state (used when stopped or signaled)
	FINISHED  // final state (used in cas of a natural exit)
	FATAL     // final state (used in case of error while starting)
)

// CmdState represents all Cmd states
type CmdState uint8

func (p CmdState) String() string {
	switch p {
	case INITIAL:
		return "initial"
	case STARTING:
		return "starting"
	case RUNNING:
		return "running"
	case STOPPING:
		return "stopping"
	case INTERRUPT:
		return "interrupted"
	case FINISHED:
		return "finished"
	case FATAL:
		return "fatal"
	default:
		return "unknown"
	}
}
