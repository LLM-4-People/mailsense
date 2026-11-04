// Package bootstrap reports errors that happen before logging works
// (bad data directory, invalid settings, unopenable store). The Windows exe
// has no console (-H windowsgui), so stderr alone would be invisible: the
// error is written to a file in the data directory and shown natively.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// ErrorFile is the startup error report, in the data directory (or the
// temp directory when the data directory itself is unknown). It is a
// bootstrap fact like settings.json: it must work before configuration loads.
const ErrorFile = "startup-error.txt"

// Report records err and shows it to the user; it returns the report path.
func Report(dataDir string, err error) string {
	if dataDir == "" {
		dataDir = os.TempDir()
	}
	path := filepath.Join(dataDir, ErrorFile)
	text := fmt.Sprintf("Mailsense could not start.\n\n%v\n\nThis report: %s\n", err, path)
	if werr := fsx.WriteFileAtomic(path, []byte(text), fsx.FilePerm, fsx.Replace{}); werr != nil {
		text += fmt.Sprintf("(the report could not be saved: %v)\n", werr)
		path = ""
	}
	notify(text)
	return path
}

// Clear removes a stale report after a successful start.
func Clear(dataDir string) { _ = os.Remove(filepath.Join(dataDir, ErrorFile)) }
