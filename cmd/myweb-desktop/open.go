package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// openPath shows a folder in the file browser, or opens a file in its app.
func openPath(p string) error {
	if _, err := os.Stat(p); err != nil {
		return errors.New(tilde(p) + " does not exist yet")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", p)
	case "darwin":
		cmd = exec.Command("open", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// pickFolder asks for a folder with the system's dialog, starting at cur.
// It returns "" when the dialog is cancelled.
func pickFolder(cur string) (string, error) {
	const prompt = "Choose the folder myweb serves"
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := `POSIX path of (choose folder with prompt "` + prompt + `"`
		if cur != "" {
			script += ` default location (POSIX file "` + appleString(cur) + `")`
		}
		cmd = exec.Command("osascript", "-e", script+")")
	case "windows":
		// the folder comes in through the environment, not the script text
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command",
			`[Console]::OutputEncoding = [Text.Encoding]::UTF8;`+
				`Add-Type -AssemblyName System.Windows.Forms;`+
				`$d = New-Object System.Windows.Forms.FolderBrowserDialog;`+
				`$d.Description = '`+prompt+`'; $d.SelectedPath = $env:MYWEB_PICK_FROM;`+
				`$f = New-Object System.Windows.Forms.Form -Property @{TopMost = $true};`+
				`if ($d.ShowDialog($f) -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }`)
		cmd.Env = append(os.Environ(), "MYWEB_PICK_FROM="+cur)
		hideWindow(cmd)
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			cmd = exec.Command(p, "--file-selection", "--directory", "--title="+prompt, "--filename="+cur+string(filepath.Separator))
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			cmd = exec.Command(p, "--getexistingdirectory", cur, "--title", prompt)
		} else {
			return "", errors.New("no folder dialog: install zenity, or run: myweb config set d DIR")
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(strings.TrimSpace(string(out))) == 0 {
			return "", nil // cancelled
		}
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", nil
	}
	if len(dir) > 1 {
		dir = strings.TrimRight(dir, `/\`)
	}
	return filepath.Abs(dir)
}

// appleString escapes s for a string in AppleScript.
func appleString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
