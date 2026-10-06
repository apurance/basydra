//go:build !windows

package client

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
