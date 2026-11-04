package layer1

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Model is one question's trained head: the weights folded into the raw
// feature space (U: (Dims + Cells) × outputs, column-major, Bias), the extra parameters
// (ordinal cut points) and the calibration (Combiner). Params keep the
// standardised weights a later run warm-starts from.
type Model struct {
	Question string    `json:"question"`
	Kind     Kind      `json:"kind"`
	Classes  int       `json:"classes"`
	Dims     int       `json:"dims"`
	Cells    int       `json:"cells"`
	Space    string    `json:"space"`  // the vector space it reads
	Layout   string    `json:"layout"` // the evidence feature layout it reads
	Spec     string    `json:"spec"`   // SpecOf its question when trained
	Params   Params    `json:"-"`
	U        []float64 `json:"-"`
	Bias     []float64 `json:"-"`
	Combine  Combiner  `json:"combine"`
	// Threshold is the question's operating point: the threshold chosen
	// on the selection labels when the certification labels certified it
	// (selective.Certify); an answer below it abstains (NoThreshold: every
	// answer abstains — nothing certified).
	Threshold float64 `json:"threshold"`
	// Holdout are the predictions it was measured on (nil: unmeasured).
	Holdout *Holdout `json:"-"`
}

// SpecOf is a question's spec digest (name, type, instructions,
// criteria, options and levels in order): a model answers only the spec
// it was trained on (an option added, renamed or reordered changes it).
func SpecOf(q *engine.Question) string {
	raw, _ := json.Marshal(q)
	return digest.Hex(raw, 16)
}

// fold computes U and Bias from Params under the standardisation of the
// rows this fit trained on.
func (m *Model) fold(std standardizer) {
	h, _ := newHead(m.Kind, m.Classes)
	f := fold(std, h.outputs(), m.Params.W, m.Params.B)
	m.U, m.Bias = f.u, f.bias
}

// head is the model's head.
func (m *Model) head() head {
	h, _ := newHead(m.Kind, m.Classes)
	return h
}

// logProbsRow writes the head's class log-probabilities of design row i
// (before calibration).
func (m *Model) logProbsRow(d *Design, i int, out []float64) {
	h := m.head()
	z := make([]float64, h.outputs())
	scores(d, folded{u: m.U, bias: m.Bias}, h.outputs(), i, z)
	h.logProbs(z, m.Params.Extra, out)
}

// blobVersion is the layout of an encoded model.
const blobVersion = 1

// Encode is the model's parameters as little-endian float32 (version,
// shape, U, Bias, Extra, W, B: the standardised weights for warm starts).
func (m *Model) Encode() []byte {
	parts := [][]float64{m.U, m.Bias, m.Params.Extra, m.Params.W, m.Params.B}
	head := []uint32{blobVersion, uint32(m.Classes), uint32(m.Dims), uint32(m.Cells)}
	n := 4 * len(head)
	for _, p := range parts {
		n += 4 + 4*len(p)
	}
	out := make([]byte, 0, n)
	for _, h := range head {
		out = binary.LittleEndian.AppendUint32(out, h)
	}
	for _, p := range parts {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(p)))
		for _, x := range p {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(float32(x)))
		}
	}
	return out
}

// ErrBadBlob: an encoded model that does not decode to the expected shape.
var ErrBadBlob = errors.New("layer1: corrupt model parameters")

// Decode reads Encode's form back into m (Kind and Classes set); every
// length must match the head's shape.
func (m *Model) Decode(b []byte) error {
	h, err := newHead(m.Kind, m.Classes)
	if err != nil {
		return err
	}
	r := reader{b: b}
	if v := r.u32(); v != blobVersion {
		return fmt.Errorf("%w: version %d", ErrBadBlob, v)
	}
	classes, dims, cells := int(r.u32()), int(r.u32()), int(r.u32())
	if classes != m.Classes {
		return fmt.Errorf("%w: %d classes, want %d", ErrBadBlob, classes, m.Classes)
	}
	m.Dims, m.Cells = dims, cells
	cols := dims + cells
	want := []int{h.outputs() * cols, h.outputs(), h.extra(), h.outputs() * cols, h.outputs()}
	parts := make([][]float64, len(want))
	for i, n := range want {
		if got := int(r.u32()); got != n || r.err != nil {
			return fmt.Errorf("%w: part %d holds %d values, want %d", ErrBadBlob, i, got, n)
		}
		parts[i] = make([]float64, n)
		for j := range parts[i] {
			x := float64(math.Float32frombits(r.u32()))
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return fmt.Errorf("%w: a value is not finite", ErrBadBlob)
			}
			parts[i][j] = x
		}
	}
	if r.err != nil || len(r.b) != r.at {
		return fmt.Errorf("%w: %d bytes, %d read", ErrBadBlob, len(r.b), r.at)
	}
	m.U, m.Bias, m.Params.Extra, m.Params.W, m.Params.B = parts[0], parts[1], parts[2], parts[3], parts[4]
	return nil
}

type reader struct {
	b   []byte
	at  int
	err error
}

func (r *reader) u32() uint32 {
	if r.err != nil || r.at+4 > len(r.b) {
		r.err = ErrBadBlob
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.at:])
	r.at += 4
	return v
}

func argmax(v []float64) int {
	best := 0
	for i := range v {
		if v[i] > v[best] {
			best = i
		}
	}
	return best
}
