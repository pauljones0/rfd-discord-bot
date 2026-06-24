package oneverycorner

import (
	"context"
	"os/exec"
)

func watchMonitorContext(ctx context.Context, cmd *exec.Cmd) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateMonitorCommand(cmd)
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}
