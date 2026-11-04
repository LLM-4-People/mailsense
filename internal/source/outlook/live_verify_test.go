//go:build windows && outlooklive

package outlook

import (
	"context"
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type folderCheck struct {
	Store        string          `json:"store"` // kind + hash of the name
	Label        string          `json:"label"` // role + path hash
	Role         mail.FolderRole `json:"role"`
	ItemsCount   int64           `json:"items_count"` // live Items.Count
	TableRows    int             `json:"table_rows"`  // live table rows now
	Stored       int             `json:"stored"`
	Missing      int             `json:"missing"`       // in Outlook, not stored
	Extra        int             `json:"extra"`         // stored, not in Outlook
	ReadMismatch int             `json:"read_mismatch"` // stored is_read ≠ live
	StampDiffers int             `json:"stamp_differs"` // stored change stamp ≠ live
	Partial      int             `json:"partial"`       // still without details
	Error        string          `json:"error,omitempty"`
}

type unreadResult struct {
	Sampled   int    `json:"sampled"`
	Unchanged int    `json:"unchanged"` // flags and stamp identical after a second ReadDetails
	Changed   int    `json:"changed"`
	Errors    int    `json:"errors"`
	Error     string `json:"error,omitempty"`
}

type liveRow struct {
	read    bool
	stamp   string
	partial bool
}

// query runs a validated read-only query (the store's choke point).
func query(t *testing.T, ctx context.Context, cfg *config.Config, q string) [][]any {
	ro, err := store.OpenReadOnly(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	res, err := ro.Query(ctx, q, cfg.Int("query.max_rows"))
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if res.Truncated { // a capped result would read as "missing" messages
		t.Fatalf("%s: more than query.max_rows (%d) rows", q, res.Limit)
	}
	return res.Rows
}

func asInt64(v any) int64 { n, _ := v.(int64); return n }

// verify compares every discovered folder with live Outlook.
func verify(t *testing.T, ctx context.Context, c *Connector, st *store.Store, cfg *config.Config) ([]folderCheck, checkTotals) {
	stored, err := st.ListFolders(ctx, c.ID())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[folderKey]int64{}
	for _, f := range stored {
		ids[folderKey{f.Folder.StoreID, f.Folder.Path}] = f.ID
	}
	folders, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []folderCheck
	var tot checkTotals
	for _, f := range folders {
		k := folderKey{f.StoreID, f.Path}
		fc := folderCheck{Store: string(f.StoreKind) + "-" + hash(f.StoreName), Label: string(f.Role) + "-" + hash(f.StoreID+f.Path), Role: f.Role}
		mine := map[string]liveRow{}
		for _, r := range query(t, ctx, cfg, fmt.Sprintf("SELECT msg_id, is_read, stamp, partial FROM messages WHERE folder_id = %d", ids[k])) {
			mine[fmt.Sprint(r[0])] = liveRow{read: asInt64(r[1]) == 1, stamp: fmt.Sprint(r[2]), partial: asInt64(r[3]) == 1}
		}
		live, n, err := liveFolder(ctx, c, c.folders[k])
		if err != nil {
			fc.Error = Code(err)
		}
		fc.ItemsCount, fc.TableRows, fc.Stored = n, len(live), len(mine)
		for id, lr := range live {
			sr, ok := mine[id]
			if !ok {
				fc.Missing++
				continue
			}
			if sr.read != lr.read {
				fc.ReadMismatch++
			}
			if sr.stamp != lr.stamp {
				fc.StampDiffers++
			}
			if sr.partial {
				fc.Partial++
			}
		}
		for id := range mine {
			if _, ok := live[id]; !ok {
				fc.Extra++
			}
		}
		tot.add(fc)
		out = append(out, fc)
	}
	return out, tot
}

// liveFolder reads Items.Count and a fresh id/stamp/flags table, through the guard.
func liveFolder(ctx context.Context, c *Connector, st folderState) (map[string]liveRow, int64, error) {
	out := map[string]liveRow{}
	var count int64
	err := c.op(ctx, func(ns Object, s settings) error {
		v, err := ns.Call("GetFolderFromID", st.entryID, st.storeID)
		if err != nil {
			return err
		}
		f := v.(Object)
		defer f.Release()
		iv, err := f.Get("Items")
		if err != nil {
			return err
		}
		items := iv.(Object)
		n, err := items.Get("Count")
		items.Release()
		if err != nil {
			return err
		}
		count = asInt(n).N
		tbl, err := openTable(f, idColumns)
		if err != nil {
			return err
		}
		defer tbl.Release()
		return readRows(tbl, s.pageRows, len(idColumns), binaryEntryID, func(values []any) bool {
			r := decode(idColumns, values, s.times)
			out[r.msg.ID] = liveRow{read: r.isRead(), stamp: r.stamp()}
			return true
		})
	})
	return out, count, err
}

// unreadProbe reads the details of up to 25 unread, complete messages a
// second time (drawn from the test's detail budget) and checks that
// neither their flags nor their modification time moved (reading Body
// must not mark mail read).
func unreadProbe(t *testing.T, ctx context.Context, c *Connector, cfg *config.Config, budget *livetest.Budget) unreadResult {
	var res unreadResult
	n := budget.Take(t, 25)
	if n == 0 {
		return res
	}
	for _, r := range query(t, ctx, cfg, fmt.Sprintf("SELECT store_id, msg_id FROM messages WHERE is_read = 0 AND partial = 0 ORDER BY received_at DESC LIMIT %d", n)) {
		ref := source.ItemRef{StoreID: fmt.Sprint(r[0]), ID: fmt.Sprint(r[1])}
		res.Sampled++
		before, err1 := itemState(ctx, c, ref)
		_, err2 := c.ReadDetails(ctx, ref)
		after, err3 := itemState(ctx, c, ref)
		switch {
		case unavailable(err1) || unavailable(err2) || unavailable(err3):
			res.Errors++
			res.Error = "outlook unavailable"
			return res // stop calling an Outlook that just failed
		case err1 != nil || err2 != nil || err3 != nil:
			res.Errors++
		case before == after:
			res.Unchanged++
		default:
			res.Changed++
		}
	}
	return res
}

// itemState is the message flags and modification time of one item.
func itemState(ctx context.Context, c *Connector, ref source.ItemRef) (string, error) {
	var out string
	err := c.op(ctx, func(ns Object, s settings) error {
		v, err := ns.Call("GetItemFromID", ref.ID, ref.StoreID)
		if err != nil {
			return err
		}
		it := v.(Object)
		defer it.Release()
		pv, err := it.Get("PropertyAccessor")
		if err != nil {
			return err
		}
		pa := pv.(Object)
		defer pa.Release()
		names := []string{propMessageFlags, propLastModified}
		raw, err := pa.Call("GetProperties", names)
		if err != nil {
			return err
		}
		vals, err := valuesOf(raw, names)
		if err != nil {
			return err
		}
		out = fmt.Sprint(asInt(vals[0]), asExact(vals[1]))
		return nil
	})
	return out, err
}

// timestampSanity counts implausible timestamps; a UTC double conversion
// shows up as sent_on hours after received_at.
func timestampSanity(t *testing.T, ctx context.Context, cfg *config.Config) map[string]int {
	checks := map[string]string{
		"messages":                     `SELECT COUNT(*) FROM messages`,
		"received_empty":               `SELECT COUNT(*) FROM messages WHERE received_at = ''`,
		"received_in_future":           `SELECT COUNT(*) FROM messages WHERE received_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '+1 day')`,
		"received_before_1995":         `SELECT COUNT(*) FROM messages WHERE received_at <> '' AND received_at < '1995'`,
		"sent_after_received_10min":    `SELECT COUNT(*) FROM messages WHERE sent_on <> '' AND received_at <> '' AND julianday(sent_on) - julianday(received_at) > 10.0/1440`,
		"sent_after_received_1h_to_6h": `SELECT COUNT(*) FROM messages WHERE sent_on <> '' AND received_at <> '' AND julianday(sent_on) - julianday(received_at) BETWEEN 1.0/24 AND 6.0/24`,
		"commit_present":               `SELECT COUNT(*) FROM messages WHERE local_commit_time <> ''`,
		"task_due_not_midnight":        `SELECT COUNT(*) FROM messages WHERE task_due_date <> '' AND substr(task_due_date, 12) <> '00:00:00Z'`,
		"partial":                      `SELECT COUNT(*) FROM messages WHERE partial = 1`,
		"body_empty_complete":          `SELECT COUNT(*) FROM messages WHERE partial = 0 AND body = ''`,
		"sender_dn":                    `SELECT COUNT(*) FROM messages WHERE lower(sender_address) LIKE '/o=%'`,
		"recipients":                   `SELECT COUNT(*) FROM recipients`,
		"subject_at_255":               `SELECT COUNT(*) FROM messages WHERE length(subject) = 255`,
		"subject_over_255":             `SELECT COUNT(*) FROM messages WHERE length(subject) > 255`,
		"held_removals":                `SELECT COUNT(*) FROM held_removals`,
	}
	out := map[string]int{}
	for name, q := range checks {
		rows := query(t, ctx, cfg, q)
		out[name] = int(asInt64(rows[0][0]))
	}
	return out
}
