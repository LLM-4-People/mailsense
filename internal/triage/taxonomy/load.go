package taxonomy

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

var (
	//go:embed default_profile.json
	defaultJSON []byte
	//go:embed mailsense_profile.json
	mailsenseJSON []byte
)

// ProfileFileSetting names the one setting Load reads. The route-key memo
// reads the same path, so the two cannot disagree.
const ProfileFileSetting = "triage.profile.file"

// profileKey is the identity of a built profile: the config snapshot (every
// setting change swaps it), the resolved file and a digest of the merged
// bytes. The digest is the content rule, not the file stamp: an edit that
// keeps the same size and mtime must still rebuild.
type profileKey struct {
	snap   *config.Snapshot
	path   string
	digest [sha256.Size]byte
}

type profileEntry struct {
	key profileKey
	p   *Profile
	err error
}

// profileCache holds the last built profile. Entries are immutable; a
// concurrent miss may build twice, but every stored entry is valid for its
// key, so an out-of-order store can only cause another rebuild.
var profileCache atomic.Pointer[profileEntry]

// Load returns the effective profile for the current configuration: the
// built-in taxonomy merged with triage.profile.file (config.OverlaySource,
// the one reader of shipped data + the user's file; missing file =
// built-in only). The parsed profile is cached because every request reads
// it and validation costs ~0.5 ms; the cache key includes the config
// snapshot and a digest of the merged bytes, so no edit or setting change
// can serve a stale profile. Every problem is reported at once.
func Load(cfg *config.Config) (*Profile, error) {
	shipped, err := builtInJSON()
	if err != nil {
		return nil, err
	}
	data, path, err := cfg.OverlaySource(ProfileFileSetting, shipped, mergedSections...)
	if err != nil {
		return nil, fmt.Errorf("taxonomy: %w", err)
	}
	key := profileKey{snap: cfg.Snapshot(), path: path, digest: sha256.Sum256(data)}
	if entry := profileCache.Load(); entry != nil && entry.key == key {
		return entry.p, entry.err
	}
	p, err := buildProfile(data, path)
	profileCache.Store(&profileEntry{key: key, p: p, err: err})
	return p, err
}

// buildProfile decodes and validates the merged document. It is the slow
// part of Load and the only part the cache stores.
func buildProfile(data []byte, path string) (*Profile, error) {
	doc, err := object(data, "profile")
	if err != nil {
		return nil, err
	}
	return parseMerged(doc, path)
}

// builtInJSON is the built-in document as JSON (Overlay's shipped data).
var builtInJSON = sync.OnceValues(func() ([]byte, error) {
	doc, err := builtIn()
	if err != nil {
		return nil, err
	}
	return json.Marshal(doc)
})

// Default is the built-in profile.
func Default() (*Profile, error) { return Parse(nil, "") }

// Parse merges an overrides document (nil = none) over the built-in one,
// validates the result and builds the typed profile.
func Parse(overrides []byte, source string) (*Profile, error) {
	doc, err := builtIn()
	if err != nil {
		return nil, err
	}
	if overrides != nil {
		over, err := object(overrides, source)
		if err != nil {
			return nil, err
		}
		doc = jsonx.MergeObject(doc, over, mergedSections...)
	}
	return parseMerged(doc, source)
}

// parseMerged validates a merged document and builds the typed profile.
func parseMerged(doc jsonx.Object, source string) (*Profile, error) {
	if problems := validate(doc); len(problems) > 0 {
		return nil, fmt.Errorf("taxonomy: invalid profile%s:\n  - %s", from(source), strings.Join(problems, "\n  - "))
	}
	p, err := build(doc)
	if err != nil {
		return nil, err
	}
	p.Source = source
	return p, nil
}

func from(source string) string {
	if source == "" {
		return ""
	}
	return " (" + source + ")"
}

func object(data []byte, where string) (jsonx.Object, error) {
	v, err := jsonx.DecodeOrdered(data)
	if err != nil {
		return nil, fmt.Errorf("taxonomy: %s: %w", where, err)
	}
	o, ok := v.(jsonx.Object)
	if !ok {
		return nil, fmt.Errorf("taxonomy: %s: the profile must be a JSON object", where)
	}
	return o, nil
}

// builtIn is the legacy taxonomy with Mailsense's additions merged in
// (decoded once; callers get a copy through MergeObject).
var builtIn = sync.OnceValues(func() (jsonx.Object, error) {
	legacy, err := object(defaultJSON, "built-in profile")
	if err != nil {
		return nil, err
	}
	ext, err := object(mailsenseJSON, "built-in Mailsense profile")
	if err != nil {
		return nil, err
	}
	return jsonx.MergeObject(legacy, ext.Delete(topLevelDocument), mergedSections...), nil
})

// legacyScaleDoc is the built-in document whose scales a dropped role keeps.
func legacyScaleDoc() jsonx.Object {
	doc, _ := builtIn()
	return doc
}
