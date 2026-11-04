//go:build !windows && !linux

package resource

import "context"

// unsupportedSampler reports that this platform has no host readings. The
// guard treats an unknown metric as allowed, so the app keeps working; the
// status projection reports the limits with unknown usage.
type unsupportedSampler struct{}

// NewHostSampler returns the platform fallback.
func NewHostSampler(string) Sampler { return unsupportedSampler{} }

func (unsupportedSampler) Sample(context.Context) (Usage, error) {
	return Usage{}, ErrUnsupported
}
