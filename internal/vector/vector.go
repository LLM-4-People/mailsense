// Package vector is the one place for embedding-vector math: finiteness
// and norm checks at the trust boundary, Matryoshka truncation with
// re-normalisation, cosine similarity, and the int8 storage form the index
// keeps (per-vector symmetric scale). Pure; no I/O.
package vector

import (
	"errors"
	"fmt"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"math"
)

// ErrInvalid marks a vector that is not usable (empty, non-finite, zero).
var ErrInvalid = errors.New("vector: invalid")

// Norm is the Euclidean length (in float64 for accuracy).
func Norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

// Check reports why v is unusable: empty, a NaN or infinite value, or a
// zero length.
func Check(v []float32) error {
	if len(v) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalid)
	}
	for i, x := range v {
		if !mathx.IsFinite(float64(x)) {
			return fmt.Errorf("%w: value %d is not finite", ErrInvalid, i)
		}
	}
	if Norm(v) == 0 {
		return fmt.Errorf("%w: zero length", ErrInvalid)
	}
	return nil
}

// Truncate keeps the first dims values (Matryoshka models front-load the
// meaning) and scales the result back to unit length. dims ≥ len(v)
// returns a normalised copy.
func Truncate(v []float32, dims int) ([]float32, error) {
	if dims <= 0 {
		return nil, fmt.Errorf("%w: %d dimensions", ErrInvalid, dims)
	}
	out := append([]float32(nil), v[:min(dims, len(v))]...)
	if err := Check(out); err != nil {
		return nil, err
	}
	n := Norm(out)
	for i := range out {
		out[i] = float32(float64(out[i]) / n)
	}
	return out, nil
}

// Dot is the inner product (the cosine of unit vectors). Lengths must match.
func Dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Cosine is the cosine similarity of two vectors of the same length
// (0 when either has no length).
func Cosine(a, b []float32) float64 {
	na, nb := Norm(a), Norm(b)
	if na == 0 || nb == 0 || len(a) != len(b) {
		return 0
	}
	return Dot(a, b) / (na * nb)
}
