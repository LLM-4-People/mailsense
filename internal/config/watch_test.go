package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// Finding (rules review #3): an edit of a data file (teachers,
// requirements, …) never started anything; the watcher publishes it as a
// change of the setting naming the file.
func TestAnEditedDataFileIsAChange(t *testing.T) {
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicChanged, 8)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"config.watch_interval": "20ms"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Options{Paths: Paths{DataDir: dir}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	stop := cfg.WatchDataFiles()
	defer stop()
	if err := os.WriteFile(cfg.Paths().Resolve(cfg.String("layer1.teachers.file")), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case ch := <-sub.C:
		if len(ch.Keys) != 1 || ch.Keys[0] != "layer1.teachers.file" {
			t.Fatalf("%+v", ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change published for an edited data file")
	}
	for _, st := range cfg.schema.Settings() {
		if st.DataFile && st.Type != TypeString {
			t.Fatalf("%s", st.Key)
		}
	}
}
