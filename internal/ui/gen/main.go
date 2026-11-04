// Command gen writes the generated /app.js bundle from js/manifest.txt.
//
// Run it with `go generate ./internal/ui` after changing any source module.
// The bundle test fails when the file on disk is stale, so a hand edit or a
// forgotten regeneration cannot ship.
package main

import (
	"log"
	"os"

	"github.com/LLM-4-People/mailsense/internal/ui"
)

func main() {
	app, err := ui.Bundle()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("app.js", app, 0o644); err != nil {
		log.Fatal(err)
	}
	style, err := ui.CSSBundle()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("app.css", style, 0o644); err != nil {
		log.Fatal(err)
	}
}
