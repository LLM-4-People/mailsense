package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Kind is the connector kind of the legacy archive importer.
const Kind = "archive"

// Connector imports one legacy JSONL export file.
type Connector struct {
	id   string
	path string
	cfg  *config.Config
	log  *slog.Logger
}

var _ source.Connector = (*Connector)(nil)

// New returns an archive connector reading path.
func New(id, path string, cfg *config.Config) (*Connector, error) {
	if id == "" || path == "" {
		return nil, errors.New("archive: id and path are required")
	}
	return &Connector{id: id, path: path, cfg: cfg, log: logx.For("source.archive").With("source", id)}, nil
}

// ID returns the source id.
func (c *Connector) ID() string { return c.id }

// Kind returns "archive".
func (c *Connector) Kind() string { return Kind }

// Capabilities: recipients are structured from the joined address lists;
// bodies are the exporter's capped preview; the file fingerprint is the
// cursor; the export kept Outlook's addressed-to-me flags, message classes,
// transport headers (cut by the exporter: declared per message) and the
// owner's actions (read state, flags, categories, last verb). The format
// names no owner: it is declared (source.SettingDeclared).
func (c *Connector) Capabilities() source.Capabilities {
	return source.Capabilities{Incremental: true, Recipients: true, AddressedToMe: true, FolderRoles: true,
		MessageClass: true, Headers: true, ReadState: true, Flags: true, Categories: true, LastVerb: true}
}

// Discover returns the two folders the legacy format knows.
func (c *Connector) Discover(context.Context) ([]mail.Folder, error) {
	var out []mail.Folder
	for _, f := range []string{legacyInbox, legacySent} {
		out = append(out, mail.Folder{SourceID: c.id, StoreName: c.storeName(), StoreKind: mail.StoreOther, Path: f,
			Role: legacyRoles[f], ItemCount: -1})
	}
	return out, nil
}

// Mailboxes is the export's one mailbox (store id ""): the file, two
// folders, items not counted (that would read the whole file), no
// reported owner.
func (c *Connector) Mailboxes() []source.Mailbox {
	return []source.Mailbox{{Key: source.StoreKey(c.id, ""), SourceID: c.id, Name: c.storeName(), Kind: mail.StoreOther,
		Folders: 2, Items: -1, IdentityComplete: true}}
}

func (c *Connector) storeName() string { return filepath.Base(c.path) }

// Sync streams the records of one folder. Lines that cannot be attributed
// to a folder (invalid JSON, unknown folder value) are reported as skips
// by the inbox pass only, so every line is counted exactly once.
func (c *Connector) Sync(ctx context.Context, folder mail.Folder, cursor source.Cursor, sink source.Sink) (source.Cursor, error) {
	if _, ok := legacyRoles[folder.Path]; !ok {
		return "", fmt.Errorf("archive: unknown folder %q", folder.Path)
	}
	before, err := fingerprint(c.path)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if source.Cursor(before) == cursor {
		c.log.Info("archive unchanged; folder skipped", "folder", folder.Path)
		return cursor, nil
	}
	snap := c.cfg.Snapshot()
	lines, err := openLines(c.path, snap.Int("archive.max_line_bytes"))
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	defer lines.close()
	mp := mapper{sourceID: c.id, times: mail.TimesFromConfig(snap), bodyCap: snap.Int("archive.legacy_body_chars"),
		headCap: snap.Int("archive.legacy_header_chars")}
	if err := c.stream(ctx, lines, folder.Path, mp, sink); err != nil {
		return "", err
	}
	after, err := fingerprint(c.path)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if after != before {
		return "", errors.New("archive: the file changed while it was read; run the sync again")
	}
	return source.Cursor(before), nil
}

func (c *Connector) stream(ctx context.Context, lines *lineSource, folder string, mp mapper, sink source.Sink) error {
	ownsUnattributed := folder == legacyInbox
	lineNo := 0
	for lines.scanner.Scan() {
		lineNo++
		if err := ctx.Err(); err != nil {
			return err
		}
		sink.Progress(source.Progress{Done: lines.bytesRead(), Total: lines.size, Unit: "bytes"})
		raw := lines.scanner.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		ref := fmt.Sprintf("line %d", lineNo)
		obj, err := rawRecord(raw)
		if err != nil {
			if ownsUnattributed {
				sink.Skip(ref, err)
			}
			continue
		}
		claimed, ok := claimedFolder(obj)
		if !ok {
			if ownsUnattributed {
				sink.Skip(ref, fmt.Errorf("field \"folder\" must be %q or %q", legacyInbox, legacySent))
			}
			continue
		}
		if claimed != folder {
			continue
		}
		rec, err := validate(obj)
		if err != nil {
			sink.Skip(ref, err)
			continue
		}
		m, err := mp.message(rec, folder)
		if err != nil {
			sink.Skip(ref, err)
			continue
		}
		if err := sink.Message(ctx, m); err != nil {
			return err
		}
	}
	if err := lines.scanner.Err(); err != nil {
		return fmt.Errorf("archive: read after line %d: %w (archive.max_line_bytes is %d)", lineNo, err, lines.maxLine)
	}
	sink.Progress(source.Progress{Done: lines.size, Total: lines.size, Unit: "bytes"})
	return nil
}
