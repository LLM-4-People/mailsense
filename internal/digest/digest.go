// Package digest is the one short-hash form: the first n hex digits of a
// SHA-256 (fingerprints, state hashes, store keys, content hashes).
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// Sum returns the first n bytes of SHA-256(data) (n ≤ 32; n ≤ 0 or beyond 32
// returns all 32). It is the raw form of Hex, for a fixed-size key.
func Sum(data []byte, n int) []byte {
	sum := sha256.Sum256(data)
	if n <= 0 || n > len(sum) {
		return sum[:]
	}
	return sum[:n]
}

// Hex returns the first n hex digits of SHA-256(data) (n ≤ 64; n ≤ 0 or
// beyond 64 returns all 64).
func Hex(data []byte, n int) string {
	h := hex.EncodeToString(Sum(data, 0))
	if n <= 0 || n > len(h) {
		return h
	}
	return h[:n]
}

// Hasher hashes what is written to it (a stream being saved).
type Hasher struct{ h hash.Hash }

// NewHasher returns a SHA-256 stream hasher.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

func (h *Hasher) Write(p []byte) (int, error) { return h.h.Write(p) }

// Hex is the full SHA-256 of what was written, in hex.
func (h *Hasher) Hex() string { return hex.EncodeToString(h.h.Sum(nil)) }

// File is the full SHA-256 of a file and its size.
func File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := NewHasher()
	n, err := io.Copy(h, f)
	return h.Hex(), n, err
}

// Of is the full SHA-256 of several byte strings, each length-prefixed
// (so the parts can never run into one another).
func Of(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		var n [8]byte
		for i, l := 0, uint64(len(p)); i < 8; i, l = i+1, l>>8 {
			n[i] = byte(l)
		}
		h.Write(n[:])
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
