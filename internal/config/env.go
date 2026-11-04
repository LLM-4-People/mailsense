package config

import "strings"

// Environment names. Every MAILSENSE_* variable is either a setting override,
// the data directory, or a secret (read by internal/secrets on non-Windows).
const (
	EnvPrefix       = "MAILSENSE_"
	EnvDataDir      = EnvPrefix + "DATA_DIR"
	EnvSecretPrefix = EnvPrefix + "SECRET_"
)

// EnvName maps a setting key to its environment override:
// log.file.max_size_mb → MAILSENSE_LOG_FILE_MAX_SIZE_MB.
func EnvName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

func isReservedEnv(name string) bool {
	return name == EnvDataDir || strings.HasPrefix(name, EnvSecretPrefix)
}

// ParseEnviron turns an os.Environ-style list into a map (a later duplicate
// wins, as with the process environment). It is the one environment parser.
func ParseEnviron(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}
