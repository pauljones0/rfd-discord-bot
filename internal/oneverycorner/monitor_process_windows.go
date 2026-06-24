//go:build windows

package oneverycorner

import "os/exec"

func prepareMonitorCommand(*exec.Cmd) {}

func terminateMonitorCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
