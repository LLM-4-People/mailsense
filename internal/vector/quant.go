package vector

import "math"

// Quantized is the int8 storage form of a vector: v[i] ≈ Values[i]·Scale.
// The scale is per vector (max |v| / 127), so a unit vector of 768
// dimensions takes 768 bytes plus one float instead of 3,072 bytes.
type Quantized struct {
	Values []int8
	Scale  float32
}

// Quantize maps v to int8 with round-half-away-from-zero; a zero vector
// gets scale 0.
func Quantize(v []float32) Quantized {
	var peak float64
	for _, x := range v {
		peak = max(peak, math.Abs(float64(x)))
	}
	q := Quantized{Values: make([]int8, len(v))}
	if peak == 0 {
		return q
	}
	q.Scale = float32(peak / 127)
	for i, x := range v {
		q.Values[i] = int8(math.Round(float64(x) / float64(q.Scale)))
	}
	return q
}

// Floats returns the approximate float vector.
func (q Quantized) Floats() []float32 {
	out := make([]float32, len(q.Values))
	for i, x := range q.Values {
		out[i] = float32(x) * q.Scale
	}
	return out
}

// Bytes is the blob form (one byte per value, two's complement).
func (q Quantized) Bytes() []byte {
	out := make([]byte, len(q.Values))
	for i, x := range q.Values {
		out[i] = byte(x)
	}
	return out
}

// FromBytes reads the blob form back.
func FromBytes(b []byte, scale float32) Quantized {
	q := Quantized{Values: make([]int8, len(b)), Scale: scale}
	for i, x := range b {
		q.Values[i] = int8(x)
	}
	return q
}

// DotInt8 is the inner product of a stored vector (values and scale) with
// a float query of the same length: the hot loop of the vector scan.
func DotInt8(values []int8, scale float32, q []float32) float32 {
	var s float32
	i := 0
	for ; i+4 <= len(values); i += 4 {
		s += float32(values[i])*q[i] + float32(values[i+1])*q[i+1] + float32(values[i+2])*q[i+2] + float32(values[i+3])*q[i+3]
	}
	for ; i < len(values); i++ {
		s += float32(values[i]) * q[i]
	}
	return s * scale
}

// NormInt8 is the Euclidean norm of stored int8 values (the scale cancels
// in a cosine, so it is left out).
func NormInt8(v []int8) float64 {
	var s int64
	for _, x := range v {
		s += int64(x) * int64(x)
	}
	return math.Sqrt(float64(s))
}

// CosineInt8 is the cosine of two stored int8 vectors of the same length
// given their norms (NormInt8, computed once per vector for repeated
// comparisons); 0 when either has no length or the lengths differ.
func CosineInt8(a, b []int8, na, nb float64) float64 {
	if len(a) != len(b) || na == 0 || nb == 0 {
		return 0
	}
	var dot int64
	for i, x := range a {
		dot += int64(x) * int64(b[i])
	}
	return float64(dot) / (na * nb)
}
