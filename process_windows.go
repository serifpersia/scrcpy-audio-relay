package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200}
}
func interruptProcess(cmd *exec.Cmd) error {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	ok, _, err := proc.Call(1, uintptr(cmd.Process.Pid))
	if ok == 0 {
		return err
	}
	return nil
}
func killProcess(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F").Run()
}
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }
