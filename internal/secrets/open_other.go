//go:build !windows

package secrets

import "github.com/LLM-4-People/mailsense/internal/fsx"

// openPlatform: outside Windows there is no DPAPI; secrets come from the
// environment and are read-only.
func openPlatform(_ string, environ []string, _ func() fsx.Replace) (Store, error) {
	return newEnvStore(environ), nil
}
