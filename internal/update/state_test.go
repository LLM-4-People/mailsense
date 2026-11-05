package update

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

func testReplace() fsx.Replace { return fsx.Replace{Attempts: 2, Delay: time.Millisecond} }

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates", "state.json")
	state, err := loadState(path, testReplace())
	if err != nil {
		t.Fatalf("a missing state file must load empty: %v", err)
	}
	release := &Release{
		Tag: "v0.2.0", Version: "0.2.0", BodyText: "notes",
		HTMLURL: "https://example.test/r", PublishedAt: "2026-11-04T10:00:00Z",
		Assets: []Asset{{Name: "a.exe", Size: 7, DownloadURL: "https://example.test/a.exe"}},
	}
	if err := state.update(func(st *State) {
		st.CheckedAt = "2026-11-04T10:00:00Z"
		st.ETag = `"v1"`
		st.Latest = release
		st.SkippedVersion = "0.2.0"
		st.SkippedAt = "2026-11-04T10:00:00Z"
		st.NotesSeenVersion = "0.1.0"
		st.ErrorCode = ""
		st.LastError = ""
		st.CheckFailures = 0
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadState(path, testReplace())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := State{
		SchemaVersion: stateSchemaVersion, CheckedAt: "2026-11-04T10:00:00Z", ETag: `"v1"`,
		Latest: release, SkippedVersion: "0.2.0", SkippedAt: "2026-11-04T10:00:00Z",
		NotesSeenVersion: "0.1.0",
	}
	if got := reloaded.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
}

func TestStateSnapshotIsIsolated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates", "state.json")
	state, err := loadState(path, testReplace())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.update(func(st *State) {
		st.Latest = &Release{Tag: "v0.2.0", Version: "0.2.0", Assets: []Asset{{Name: "a.exe"}}}
	}); err != nil {
		t.Fatal(err)
	}
	snap := state.snapshot()
	snap.Latest.BodyText = "mutated"
	snap.Latest.Assets[0].Name = "mutated"
	again := state.snapshot()
	if again.Latest.BodyText != "" || again.Latest.Assets[0].Name != "a.exe" {
		t.Fatalf("snapshot shared state: %+v", again.Latest)
	}
}

func TestStateCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), fsx.DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), fsx.FilePerm); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path, testReplace())
	if err == nil {
		t.Fatal("a corrupt state file must be reported")
	}
	if got := state.snapshot(); !reflect.DeepEqual(got, State{}) {
		t.Fatalf("corrupt state loaded as %+v", got)
	}
	// The next write rebuilds a clean file.
	if err := state.update(func(st *State) { st.ETag = `"v2"` }); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadState(path, testReplace())
	if err != nil {
		t.Fatalf("reload after a repair: %v", err)
	}
	if got := reloaded.snapshot(); got.ETag != `"v2"` || got.SchemaVersion != stateSchemaVersion {
		t.Fatalf("repaired state = %+v", got)
	}
}

func TestStateOtherSchemaVersionStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates", "state.json")
	if err := fsx.WriteJSON(path, map[string]any{"schema_version": stateSchemaVersion + 1, "etag": `"future"`}, fsx.FilePerm, testReplace()); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path, testReplace())
	if err == nil {
		t.Fatal("a future schema version must be reported")
	}
	if got := state.snapshot(); !reflect.DeepEqual(got, State{}) {
		t.Fatalf("future state loaded as %+v", got)
	}
}
