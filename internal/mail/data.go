package mail

import (
	"sync"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// DataDigest is the digest of what the shipped text data of this package
// means (digest.Content: scripts, folding, body cleaning, message kinds,
// subject prefixes, question marks; documentation and formatting aside):
// views computed from it (evidence values, vectors' text) are versioned
// with it, so changed data recomputes them and a doc edit changes nothing.
var DataDigest = sync.OnceValue(func() string {
	return digest.Content(dataFiles()...)
})

// dataFiles are the data files DataDigest reads, in a fixed order.
func dataFiles() [][]byte {
	return [][]byte{scriptsJSON, foldJSON, cleanJSON, kindsJSON, prefixesJSON, punctuationJSON}
}
