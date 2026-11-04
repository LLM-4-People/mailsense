// Command goldseal seals the test split of a gold sample directory
// (internal/goldset.Seal) and prints what it did as counts only (run by
// scripts/gold-seal.sh).
//
//	goldseal <gold dir>
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/LLM-4-People/mailsense/internal/goldset"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: goldseal <gold dir>")
		os.Exit(2)
	}
	rep, err := goldset.Seal(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
}
