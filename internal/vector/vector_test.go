package vector

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

func TestCheckRejectsUnusableVectors(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	for name, v := range map[string][]float32{"empty": nil, "nan": {1, nan}, "inf": {inf, 0}, "zero": {0, 0, 0}} {
		if err := Check(v); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Check([]float32{0, 3, 4}); err != nil {
		t.Fatal(err)
	}
}

func TestTruncateRenormalises(t *testing.T) {
	v := []float32{0.6, 0.8, 0, 0}
	got, err := Truncate([]float32{3, 4, 12, 0}, 2)
	if err != nil || math.Abs(Norm(got)-1) > 1e-6 || math.Abs(Dot(got, v[:2])-1) > 1e-6 {
		t.Fatalf("truncated to 2 and unit length: %v %v", got, err)
	}
	if full, _ := Truncate([]float32{3, 4}, 10); len(full) != 2 || math.Abs(Norm(full)-1) > 1e-6 {
		t.Fatalf("more dimensions than the vector keeps all: %v", full)
	}
	if _, err := Truncate([]float32{0, 0, 1}, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a prefix of zeros cannot be normalised: %v", err)
	}
	if _, err := Truncate([]float32{1}, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero dimensions is misuse")
	}
	src := []float32{3, 4}
	_, _ = Truncate(src, 2)
	if src[0] != 3 {
		t.Fatal("the input is never modified")
	}
}

func TestCosine(t *testing.T) {
	if c := Cosine([]float32{1, 0}, []float32{2, 0}); math.Abs(c-1) > 1e-9 {
		t.Fatal(c)
	}
	if c := Cosine([]float32{1, 0}, []float32{0, 0}); c != 0 {
		t.Fatal("zero vector")
	}
	if c := Cosine([]float32{1, 0}, []float32{1}); c != 0 {
		t.Fatal("length mismatch")
	}
}

func TestQuantizeRoundTripsWithinHalfAStep(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	v := make([]float32, 768)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	v, _ = Truncate(v, len(v))
	q := Quantize(v)
	back := FromBytes(q.Bytes(), q.Scale).Floats()
	for i := range v {
		if math.Abs(float64(back[i]-v[i])) > float64(q.Scale)/2+1e-7 {
			t.Fatalf("value %d: %v vs %v (step %v)", i, back[i], v[i], q.Scale)
		}
	}
	if c := Cosine(v, back); c < 0.999 {
		t.Fatalf("int8 keeps the direction: %v", c)
	}
	if d, want := DotInt8(q.Values, q.Scale, v), Dot(v, v); math.Abs(float64(d)-want) > 1e-3 {
		t.Fatalf("DotInt8 %v vs %v", d, want)
	}
	if z := Quantize(make([]float32, 3)); z.Scale != 0 || len(z.Values) != 3 {
		t.Fatalf("zero vector: %+v", z)
	}
	odd := Quantize([]float32{1, -1, 0.5})
	if d := DotInt8(odd.Values, odd.Scale, []float32{1, 1, 1}); math.Abs(float64(d)-0.5) > 0.01 {
		t.Fatalf("tail loop: %v", d)
	}
}

// Review L0 (rules #13): the one int8 cosine equals the float cosine of the
// dequantized vectors (the scale cancels), and refuses mismatched input.
func TestCosineInt8(t *testing.T) {
	a, b := Quantize([]float32{0.6, 0.8, 0}), Quantize([]float32{0.8, 0.6, 0})
	got := CosineInt8(a.Values, b.Values, NormInt8(a.Values), NormInt8(b.Values))
	if want := Cosine(a.Floats(), b.Floats()); math.Abs(got-want) > 1e-6 || math.Abs(got-0.96) > 0.01 {
		t.Fatalf("cosine %v, want %v", got, want)
	}
	if CosineInt8(a.Values, b.Values[:2], 1, 1) != 0 || CosineInt8(a.Values, b.Values, 0, 1) != 0 {
		t.Fatal("mismatched lengths or a zero vector give 0")
	}
}
