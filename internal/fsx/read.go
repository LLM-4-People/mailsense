package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ReadOptional reads a file that may be absent: (nil, false, nil) when it
// does not exist; any other failure is an error.
func ReadOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Stamp identifies a file's version by size and modification time ("-"
// when it cannot be read): derived data built from a data file is rebuilt
// when its stamp changes.
func Stamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d.%d", fi.Size(), fi.ModTime().UnixNano())
}
