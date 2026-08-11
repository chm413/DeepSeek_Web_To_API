//go:build !windows

package xrayproxy

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
