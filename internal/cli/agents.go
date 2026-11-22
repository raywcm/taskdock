package cli

import (
	"os/exec"
)

func agentctlPath() string {
	path, err := exec.LookPath("agentctl")
	if err != nil {
		return ""
	}
	return path
}
