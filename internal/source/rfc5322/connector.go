// Package rfc5322 is the connector for plain Internet mail files: an mbox
// (RFC 4155, also Gmail/Thunderbird exports), a maildir (one file per
// message), or .eml files, in any directory tree — MIME bodies in any
// registered charset, attachment metadata, Gmail labels as further
// folders. It is how public corpora and exports from any provider get in.
// The files name no owner and no folder roles: the owner is declared
// (source.SettingDeclared) and roles come from rfc5322.folder_roles,
// never from a folder's name; only Gmail's SYSTEM labels (Spam, Trash…,
// formats.json) carry their documented roles. Every change made to a
// message while reading it is recorded on it (mail.Alteration).
package rfc5322

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Kind is the connector kind.
const Kind = "rfc5322"

// Origin is the Message.Origin of its mail.
const Origin = "rfc5322"

// storeID: a source is one mailbox store.
const storeID = ""

// Connector reads one mail file or directory tree, read-only.
type Connector struct {
	id, root string
	cfg      *config.Config
	log      *slog.Logger
	folders  []folderSpec
}

var _ source.Connector = (*Connector)(nil)

// New returns a connector over root (a file or a directory).
func New(id, root string, cfg *config.Config) (*Connector, error) {
	if id == "" || root == "" || cfg == nil {
		return nil, errors.New("rfc5322: id, path and config are required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Connector{id: id, root: abs, cfg: cfg, log: logx.For("source.rfc5322").With("source", id)}, nil
}

// ID returns the source id.
func (c *Connector) ID() string { return c.id }

// Kind returns "rfc5322".
func (c *Connector) Kind() string { return Kind }

// Capabilities: bodies, recipients, headers and attachment metadata;
// complete snapshots (removals); further folders from Gmail labels; the
// read state (maildir S flag, the mbox Status header, Gmail's Unread
// label). No owner identity, no addressed-to-me flags, no folder roles,
// no message classes (kinds come from the headers), no flags, categories
// or verbs.
func (c *Connector) Capabilities() source.Capabilities {
	return source.Capabilities{Incremental: true, Bodies: true, Recipients: true, Deletes: true, Headers: true,
		MultiFolder: true, Attachments: true, ReadState: true}
}

// Discover lists the folders of the tree (listing is cheap: no message
// is read; the syncer keeps them only when the mailbox is picked).
func (c *Connector) Discover(context.Context) ([]mail.Folder, error) {
	specs, err := scanLayout(c.root)
	if err != nil {
		return nil, fmt.Errorf("rfc5322: %w", err)
	}
	c.folders = specs
	roles := c.roles()
	var out []mail.Folder
	for _, f := range specs {
		out = append(out, mail.Folder{SourceID: c.id, StoreID: storeID, StoreName: c.name(), StoreKind: mail.StoreOther,
			Path: f.Path, Role: roleOf(roles, f.Path), ItemCount: -1, RemoteID: f.FSPath})
	}
	return out, nil
}

// Mailboxes is the one mailbox of the tree; it reports no owner.
func (c *Connector) Mailboxes() []source.Mailbox {
	return []source.Mailbox{{Key: source.StoreKey(c.id, storeID), SourceID: c.id, StoreID: storeID, Name: c.name(),
		Kind: mail.StoreOther, Cached: true, Folders: len(c.folders), Items: -1, IdentityComplete: true}}
}

func (c *Connector) name() string { return filepath.Base(c.root) }

// roles reads rfc5322.folder_roles ("<folder path>=<role>").
func (c *Connector) roles() map[string]mail.FolderRole {
	out := map[string]mail.FolderRole{}
	for _, p := range c.cfg.Strings(SettingFolderRoles) {
		path, role, _ := strings.Cut(p, "=")
		out[path] = mail.FolderRole(role)
	}
	return out
}

func roleOf(roles map[string]mail.FolderRole, path string) mail.FolderRole {
	if r, ok := roles[path]; ok {
		return r
	}
	return mail.RoleOther
}

// Sync reads one folder: unchanged (fingerprint) → nothing; otherwise a
// complete snapshot of its ids and stamps, then the wanted messages.
func (c *Connector) Sync(ctx context.Context, folder mail.Folder, cursor source.Cursor, sink source.Sink) (source.Cursor, error) {
	spec, ok := c.spec(folder.Path)
	if !ok {
		return "", fmt.Errorf("rfc5322: unknown folder %q", folder.Path)
	}
	before, err := fingerprint(spec)
	if err != nil {
		return "", err
	}
	if source.Cursor(before) == cursor {
		return cursor, nil
	}
	snap := c.cfg.Snapshot()
	items, err := listItems(spec, int64(snap.Int(SettingMaxMessageMB))<<20)
	if err != nil {
		return "", err
	}
	versions := make([]source.Version, len(items))
	for i, it := range items {
		versions[i] = source.Version{ID: it.ID, Stamp: it.Stamp}
	}
	want, err := sink.Snapshot(ctx, versions, true)
	if err != nil {
		return "", err
	}
	lim, err := limitsOf(snap)
	if err != nil {
		return "", err
	}
	roles := c.roles()
	for i, it := range items {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		sink.Progress(source.Progress{Done: int64(i + 1), Total: int64(len(items)), Unit: "items"})
		if !want[it.ID] {
			continue
		}
		m, err := c.message(it, folder, lim, roles)
		if err != nil {
			sink.Skip(it.ID, err)
			continue
		}
		if err := sink.Message(ctx, m); err != nil {
			return "", err
		}
	}
	if after, err := fingerprint(spec); err != nil || after != before {
		return "", err // changed while read: no cursor, read again next run
	}
	return source.Cursor(before), nil
}

func (c *Connector) spec(path string) (folderSpec, bool) {
	for _, f := range c.folders {
		if f.Path == path {
			return f, true
		}
	}
	return folderSpec{}, false
}

// message reads and parses one item into the folder.
func (c *Connector) message(it item, folder mail.Folder, lim limits, roles map[string]mail.FolderRole) (mail.Message, error) {
	data, err := it.read()
	if err != nil {
		return mail.Message{}, err
	}
	m, h, err := parse(data, lim)
	if err != nil {
		return mail.Message{}, err
	}
	m.SourceID, m.StoreID, m.StoreName, m.FolderPath, m.FolderRole = c.id, storeID, c.name(), folder.Path, folder.Role
	m.ID, m.Origin, m.Stamp, m.IsRead = it.ID, Origin, it.Stamp, it.Seen || strings.ContainsRune(h.Get("Status"), 'R')
	if it.unsplit > 0 {
		m.Alter(mail.AltMboxUnsplit, strconv.Itoa(it.unsplit))
	}
	labels := gmailLabels(h.Get("X-Gmail-Labels"), newWordDecoder(&m))
	if len(labels) > 0 { // a Gmail export: the read state is Gmail's (formats.json gmail_unread_label)
		m.IsRead = !slices.Contains(labels, formats().unread)
	}
	for _, label := range labels {
		if label != folder.Path && label != formats().unread {
			m.Folders = append(m.Folders, mail.FolderRef{Path: label, Role: labelRole(roles, label)})
		}
	}
	return m, nil
}

// limitsOf reads the per-message rules of a sync.
func limitsOf(snap *config.Snapshot) (limits, error) {
	raw, err := htmlindex.Get(snap.String(SettingRawCharset))
	if err != nil {
		return limits{}, fmt.Errorf("rfc5322: %s %q is not a known charset: %w", SettingRawCharset, snap.String(SettingRawCharset), err)
	}
	return limits{Limits: mail.LimitsFromConfig(snap), maxDepth: snap.Int(SettingMimeMaxDepth),
		rawCharset: raw, times: mail.TimesFromConfig(snap)}, nil
}

// labelRole is a Gmail label's role: rfc5322.folder_roles first, then the
// roles of Gmail's system labels (formats.json), else other.
func labelRole(roles map[string]mail.FolderRole, label string) mail.FolderRole {
	if r, ok := roles[label]; ok {
		return r
	}
	if r, ok := formats().labels[label]; ok {
		return r
	}
	return mail.RoleOther
}

// gmailLabels are the labels of a Gmail export message (the value of its
// X-Gmail-Labels header, read from the whole header block: comma-separated,
// quoted when they hold a comma, RFC 2047 words when not ASCII): each is a
// further folder of the message.
func gmailLabels(v string, dec *mime.WordDecoder) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range v + "," {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			if l := strings.TrimSpace(cur.String()); l != "" {
				out = append(out, decodeWords(dec, l))
			}
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return out
}
