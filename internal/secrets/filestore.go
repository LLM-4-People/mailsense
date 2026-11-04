package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// cipher seals the whole secret file (DPAPI on Windows, a fake in tests).
type cipher interface {
	protect(plain []byte) ([]byte, error)
	unprotect(sealed []byte) ([]byte, error)
}

// fileStore keeps all secrets as one sealed JSON object, rewritten atomically.
type fileStore struct {
	mu     sync.Mutex
	path   string
	cipher cipher
	policy func() fsx.Replace
	values map[string]string
}

func openFileStore(path string, c cipher, policy func() fsx.Replace) (*fileStore, error) {
	s := &fileStore{path: path, cipher: c, policy: policy, values: map[string]string{}}
	sealed, ok, err := fsx.ReadOptional(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s, nil
	}
	plain, err := c.unprotect(sealed)
	if err != nil {
		return nil, fmt.Errorf("secrets: cannot decrypt %s (another Windows user or a corrupt file): %w", path, err)
	}
	if err := json.Unmarshal(plain, &s.values); err != nil || s.values == nil {
		return nil, fmt.Errorf("secrets: %s is not a valid secret file (want a JSON object): %v", path, err)
	}
	for name := range s.values {
		if err := ValidName(name); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *fileStore) Get(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return lookup(s.values, name)
}

func (s *fileStore) Set(name, value string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if value == "" {
		return errors.New("secrets: empty value (use Delete)")
	}
	return s.update(func(m map[string]string) { m[name] = value })
}

func (s *fileStore) Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	s.mu.Lock()
	_, err := lookup(s.values, name)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.update(func(m map[string]string) { delete(m, name) })
}

func (s *fileStore) Kind() string { return "dpapi" }

func (s *fileStore) ReadOnly() bool { return false }

func (s *fileStore) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedNames(s.values), nil
}

// update applies edit to a copy, persists it, and only then makes it current.
func (s *fileStore) update(edit func(map[string]string)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := maps.Clone(s.values)
	edit(next)
	plain, err := json.Marshal(next)
	if err != nil {
		return err
	}
	sealed, err := s.cipher.protect(plain)
	if err != nil {
		return fmt.Errorf("secrets: encrypt: %w", err)
	}
	if err := fsx.WriteFileAtomic(s.path, sealed, fsx.FilePerm, s.policy()); err != nil {
		return fmt.Errorf("secrets: save: %w", err)
	}
	s.values = next
	return nil
}
