package secrets

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// envStore reads MAILSENSE_SECRET_<NAME> variables (development only).
type envStore struct{ values map[string]string }

func newEnvStore(environ []string) *envStore {
	s := &envStore{values: map[string]string{}}
	for k, v := range config.ParseEnviron(environ) {
		if name, ok := strings.CutPrefix(k, config.EnvSecretPrefix); ok && ValidName(name) == nil && v != "" {
			s.values[name] = v
		}
	}
	return s
}

func (s *envStore) Get(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	return lookup(s.values, name)
}

func (s *envStore) Set(name, _ string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return ErrReadOnly
}

func (s *envStore) Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return ErrReadOnly
}

func (s *envStore) Kind() string { return "env" }

func (s *envStore) ReadOnly() bool { return true }

func (s *envStore) List() ([]string, error) {
	return sortedNames(s.values), nil
}
