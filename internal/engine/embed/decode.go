package embed

import (
	"fmt"
	"math"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// decode reads an OpenAI-compatible embeddings response strictly: one
// item per input with a unique index, each a list of finite JSON numbers
// of the model's native length and unit norm (within the provider's
// tolerance; anything else is a broken backend such as an fp16 overflow),
// then cut to dims and re-normalised. Tokens come from usage.prompt_tokens
// (0 when the server reports none).
func decode(raw []byte, n int, spec *engine.EmbeddingSpec, dims int) ([][]float32, int64, error) {
	v, err := jsonx.DecodeOne(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrContract, err)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("%w: response is %s, not an object", ErrContract, jsonx.Kind(v))
	}
	data, ok := doc["data"].([]any)
	if !ok || len(data) != n {
		return nil, 0, fmt.Errorf("%w: data must list %d embeddings, got %s", ErrContract, n, describeList(doc["data"]))
	}
	out := make([][]float32, n)
	for i, item := range data {
		idx, vec, err := item1(item, n, spec, dims)
		if err == nil && out[idx] != nil {
			err = fmt.Errorf("index %d appears twice", idx)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("%w: data[%d]: %v", ErrContract, i, err)
		}
		out[idx] = vec
	}
	return out, promptTokens(doc), nil
}

func item1(item any, n int, spec *engine.EmbeddingSpec, dims int) (int, []float32, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return 0, nil, fmt.Errorf("is %s, not an object", jsonx.Kind(item))
	}
	idx, ok := jsonx.Int(m["index"])
	if !ok || idx < 0 || idx >= int64(n) {
		return 0, nil, fmt.Errorf("index must be an integer in [0, %d)", n)
	}
	values, ok := m["embedding"].([]any)
	if !ok || len(values) != spec.Dimensions {
		return 0, nil, fmt.Errorf("embedding must be %d numbers, got %s", spec.Dimensions, describeList(m["embedding"]))
	}
	vec := make([]float32, len(values))
	for j, x := range values {
		f, ok := jsonx.Finite(x)
		if !ok {
			return 0, nil, fmt.Errorf("embedding value %d is not a finite number", j)
		}
		vec[j] = float32(f)
	}
	if norm := vector.Norm(vec); math.Abs(norm-1) > spec.NormTolerance {
		return 0, nil, fmt.Errorf("embedding length %.4f is not 1 ± %v", norm, spec.NormTolerance)
	}
	cut, err := vector.Truncate(vec, dims)
	return int(idx), cut, err
}

func promptTokens(doc map[string]any) int64 {
	usage, _ := doc["usage"].(map[string]any)
	t, ok := jsonx.Int(usage["prompt_tokens"])
	if !ok || t < 0 {
		return 0
	}
	return t
}

func describeList(v any) string {
	if l, ok := v.([]any); ok {
		return fmt.Sprintf("%d", len(l))
	}
	return jsonx.Kind(v)
}
