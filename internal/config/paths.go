package config

import (
	"errors"
	"fmt"
	"path/filepath"
)

const (
	appDirWindows = "Mailsense"
	appDirXDG     = "mailsense"
	settingsFile  = "settings.json"
)

// Paths locates the data directory and the files inside it.
type Paths struct{ DataDir string }

// ResolvePaths picks the data directory: MAILSENSE_DATA_DIR, else
// %LOCALAPPDATA%\Mailsense on Windows, else $XDG_DATA_HOME/mailsense or
// ~/.local/share/mailsense.
// The result is always absolute: a relative MAILSENSE_DATA_DIR is resolved
// against the working directory (SQLite would read "file://data/…" as a host).
func ResolvePaths(environ []string, goos, home string) (Paths, error) {
	dir, err := dataDir(ParseEnviron(environ), goos, home)
	if err != nil {
		return Paths{}, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, fmt.Errorf("config: data directory %q: %w", dir, err)
	}
	return Paths{DataDir: abs}, nil
}

func dataDir(env map[string]string, goos, home string) (string, error) {
	if dir := env[EnvDataDir]; dir != "" {
		return dir, nil
	}
	if goos == "windows" {
		if base := env["LOCALAPPDATA"]; base != "" {
			return filepath.Join(base, appDirWindows), nil
		}
		return "", errors.New("config: LOCALAPPDATA is not set; set " + EnvDataDir)
	}
	if base := env["XDG_DATA_HOME"]; filepath.IsAbs(base) {
		return filepath.Join(base, appDirXDG), nil
	}
	if home == "" {
		return "", errors.New("config: no home directory; set " + EnvDataDir)
	}
	return filepath.Join(home, ".local", "share", appDirXDG), nil
}

// SettingsFile is the user settings layer.
func (p Paths) SettingsFile() string { return filepath.Join(p.DataDir, settingsFile) }

// Resolve makes a configured path absolute: relative paths live in the data directory.
func (p Paths) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(p.DataDir, path)
}
