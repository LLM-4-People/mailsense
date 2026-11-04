// Command datagen generates Mailsense's shipped language-neutral data
// files from pinned public sources (run by scripts/update-unicode-data.sh
// and scripts/update-prefixes.sh, which download and pin the sources;
// this program only transforms them, deterministically).
//
//	datagen unicode <source dir> <internal/mail dir>
//	datagen prefixes <wikitext file> <internal/mail dir>
//	datagen locale <source dir> <internal/locale dir>
//	datagen locale-ids <source dir> <internal/locale dir>   (the CLDR main files the locale generator reads, one per line)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 4 {
		fail(fmt.Errorf("usage: datagen unicode|prefixes|locale|locale-ids <source> <output dir>"))
	}
	var err error
	switch os.Args[1] {
	case "unicode":
		err = unicodeData(os.Args[2], os.Args[3])
	case "prefixes":
		err = prefixData(os.Args[2], os.Args[3])
	case "locale":
		err = localeData(os.Args[2], os.Args[3])
	case "locale-ids":
		err = localeIDs(os.Args[2], os.Args[3])
	default:
		err = fmt.Errorf("unknown generator %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "datagen:", err)
	os.Exit(1)
}

// readJSON decodes an existing output (its hand-kept keys are carried
// over); a missing file is not an error.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes v indented, without HTML escaping, newline-terminated.
func writeJSON(dir, name string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o644)
}
