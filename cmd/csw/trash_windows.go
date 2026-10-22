package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// The path travels in an env var so it never needs PowerShell quoting.
// Network and removable drives have no Recycle Bin, where SendToRecycleBin
// would delete for good, so the script refuses anything but a fixed disk.
const recycleScript = `$ErrorActionPreference = 'Stop'; $p = $env:CSW_TRASH_PATH; ` +
	`$root = [System.IO.Path]::GetPathRoot($p); ` +
	`if ($root.StartsWith('\\') -or ([System.IO.DriveInfo]::new($root)).DriveType -ne 'Fixed') ` +
	`{ throw "$p is not on a local fixed disk, which has no Recycle Bin; move or delete it yourself" }; ` +
	`Add-Type -AssemblyName Microsoft.VisualBasic; ` +
	`[Microsoft.VisualBasic.FileIO.FileSystem]::DeleteDirectory($p, 'OnlyErrorDialogs', 'SendToRecycleBin')`

func moveToTrash(path, _ string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", recycleScript)
	cmd.Env = append(os.Environ(), "CSW_TRASH_PATH="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("recycle %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return "Recycle Bin", nil
}
