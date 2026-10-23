//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd)       { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }
func killProcess(cmd *exec.Cmd) error      { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
func shutdownSignals() []os.Signal         { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
