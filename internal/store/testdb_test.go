package store

import (
	"context"
	"os"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// migratedTemplate is one migrated, empty database built once per test
// process. Every test copies it instead of replaying the 38 migrations (and
// their full-text index setup) on each of the suite's opens. The bytes are
// copied, so each test still owns a private database file.
var migratedTemplate = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "mailsense-store-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		return nil, err
	}
	s, err := Open(context.Background(), cfg, nil, nil)
	if err != nil {
		return nil, err
	}
	path := Path(cfg)
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
})
