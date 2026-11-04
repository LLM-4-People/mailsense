// Command gensite renders the public site and brand assets from one source.
//
//	go run ./cmd/gensite -write   # write every generated file
//	go run ./cmd/gensite -check   # fail when a file is stale
//
// The source is docs/site.json; the outputs are listed in site.GeneratedPaths.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/site"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "compare the generated files without writing")
	write := flag.Bool("write", false, "write every generated file (the default)")
	pages := flag.String("pages", "", "print the shipped docs pages for a language (en or fr) and exit")
	flag.Parse()

	if *pages != "" {
		if *pages != "en" && *pages != "fr" {
			fmt.Fprintln(os.Stderr, "pages language must be en or fr")
			os.Exit(2)
		}
		for _, name := range site.PublicPagePaths(*pages) {
			fmt.Println(name)
		}
		return
	}
	if *check && *write {
		fmt.Fprintln(os.Stderr, "choose -write or -check, not both")
		os.Exit(2)
	}
	if *check {
		stale, err := site.Check(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(stale) > 0 {
			fmt.Fprintf(os.Stderr, "generated public surface is stale:\n  %s\nrun: go run ./cmd/gensite -write\n", strings.Join(stale, "\n  "))
			os.Exit(1)
		}
		fmt.Printf("generated public surface ok (%d files)\n", len(site.GeneratedPaths()))
		return
	}

	written, err := site.Write(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range written {
		fmt.Printf("wrote %s\n", name)
	}
}
