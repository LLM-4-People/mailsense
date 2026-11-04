//go:build !windows

package outlook

// newHost: COM exists only on Windows.
func newHost() (host, error) { return nil, ErrUnsupported }
