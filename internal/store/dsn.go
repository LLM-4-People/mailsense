package store

import (
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// fileURI turns a filesystem path into a SQLite file: URI with every special
// character percent-encoded (a raw '?' or '#' in the path once silently
// opened a different, empty database — PORTING §10).
func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if len(p) >= 2 && p[1] == ':' { // Windows drive letter: file:///C:/…
		p = "/" + p
	}
	return "file://" + (&url.URL{Path: p}).EscapedPath()
}

// dsn builds a modernc DSN: the URI plus driver/SQLite parameters.
func dsn(path string, params url.Values) string {
	return fileURI(path) + "?" + params.Encode()
}

// baseParams are the parameters shared by the read-write and read-only pools.
func baseParams(busy time.Duration) url.Values {
	return url.Values{
		"_busy_timeout": {strconv.FormatInt(busy.Milliseconds(), 10)},
		"_pragma":       {"foreign_keys(1)"},
	}
}

// storagePragmas are the per-connection WAL and page-cache settings. They
// belong in the DSN: a PRAGMA statement run once after Open reaches only
// whichever pooled connection the pool handed out, so the other pooled
// connections would keep SQLite's defaults. All values are settings, so the
// owner is the settings table and this function is its only reader.
func storagePragmas(cfg *config.Config) []string {
	var out []string
	if pages := cfg.Int(SettingWALAutocheckpoint); pages > 0 {
		out = append(out, "wal_autocheckpoint("+strconv.Itoa(pages)+")")
	}
	if limit := cfg.Int(SettingWALCheckpointMB); limit > 0 {
		out = append(out, "journal_size_limit("+strconv.FormatInt(int64(limit)<<20, 10)+")")
	}
	// cache_size's argument is KiB, and its sign selects the unit: negative
	// means KiB and is the form SQLite keeps as a per-connection default.
	if mb := cfg.Int(SettingCacheSizeMB); mb > 0 {
		out = append(out, "cache_size(-"+strconv.Itoa(mb*1024)+")")
	}
	// mmap_size is bytes; 0 is left out so SQLite keeps its compile-time
	// default instead of disabling the mapping.
	if mb := cfg.Int(SettingMmapSizeMB); mb > 0 {
		out = append(out, "mmap_size("+strconv.FormatInt(int64(mb)<<20, 10)+")")
	}
	return out
}
