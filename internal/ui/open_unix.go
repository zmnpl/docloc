//go:build !windows

package ui

import (
	"os/exec"
	"runtime"
	"syscall"
)

// openFile hands the file to the desktop's default application. The child
// gets its own session so it survives docloc exiting.
func openFile(path string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
