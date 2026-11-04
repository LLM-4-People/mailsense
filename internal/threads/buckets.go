package threads

import (
	"fmt"
	"strconv"
)

// NoReply labels the inbox messages the owner did not answer.
const NoReply = "no reply"

// buckets counts latencies (seconds) into the configured minute bounds:
// [60, 240, 1440, 4320] gives <1h, 1h-4h, 4h-1d, 1d-3d, >3d, plus no reply.
func buckets(latencies []float64, bounds []int, unanswered int) []Bucket {
	out := make([]Bucket, 0, len(bounds)+2)
	for i, b := range bounds {
		label := "<" + Span(b)
		if i > 0 {
			label = Span(bounds[i-1]) + "-" + Span(b)
		}
		out = append(out, Bucket{Label: label, UpToMinute: b})
	}
	out = append(out, Bucket{Label: ">" + Span(bounds[len(bounds)-1])})
	fb := Floats(bounds)
	for _, s := range latencies {
		out[BucketIndex(s/60, fb)].Count++
	}
	return append(out, Bucket{Label: NoReply, Count: unanswered})
}

// BucketIndex is the bucket of a response time in minutes (bounds are
// exclusive upper limits; past the last bound: len(bounds)).
func BucketIndex(minutes float64, bounds []float64) int {
	for i, b := range bounds {
		if minutes < b {
			return i
		}
	}
	return len(bounds)
}

// Floats converts minute bounds of the settings to bucket bounds.
func Floats(bounds []int) []float64 {
	out := make([]float64, len(bounds))
	for i, b := range bounds {
		out[i] = float64(b)
	}
	return out
}

// BucketLabels lists the labels of the response buckets (without "no reply").
func BucketLabels(bounds []int) []string {
	var out []string
	for _, b := range buckets(nil, bounds, 0) {
		if b.Label != NoReply {
			out = append(out, b.Label)
		}
	}
	return out
}

// Span renders a minute count compactly: 45m, 4h, 1d, 90m.
func Span(minutes int) string {
	switch {
	case minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + "d"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "h"
	}
	return fmt.Sprintf("%dm", minutes)
}
