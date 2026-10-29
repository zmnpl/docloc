package ui

import "os/exec"

// openFile hands the file to the default application.
func openFile(path string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
