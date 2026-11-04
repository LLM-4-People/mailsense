package outlook

import (
	"errors"
	"fmt"
)

// errLayout means Table.GetArray did not return rows of the requested
// columns (a SAFEARRAY read in the wrong dimension order would show here).
var errLayout = errors.New("outlook: Table.GetArray returned an unexpected layout")

// entryKey is how column 0 of a table spells an item's EntryID: the mail
// tables return the binary long-term id, an address book's table returns the
// EntryID as a string (contacts.go).
type entryKey int

const (
	binaryEntryID entryKey = iota // []byte: the mail tables' long-term EntryID
	textEntryID                   // string: a Contacts table's EntryID
)

// openTable opens a folder's contents table with exactly the given columns.
func openTable(folder Object, cols []column) (Object, error) {
	names := make([]string, len(cols))
	for i, col := range cols {
		names[i] = col.name
	}
	return openTableColumns(folder, names)
}

// openTableColumns opens a folder's contents table with exactly the given
// column names, for a reader that decodes rows into its own shape (the
// address book's contactRow).
func openTableColumns(folder Object, names []string) (Object, error) {
	v, err := folder.Call("GetTable", "", olUserItems)
	if err != nil {
		return nil, err
	}
	t := v.(Object)
	if err := setColumns(t, names); err != nil {
		t.Release()
		return nil, err
	}
	return t, nil
}

func setColumns(t Object, names []string) error {
	v, err := t.Get("Columns")
	if err != nil {
		return err
	}
	c := v.(Object)
	defer c.Release()
	if _, err := c.Call("RemoveAll"); err != nil {
		return err
	}
	for _, name := range names {
		v, err := c.Call("Add", name)
		if err != nil {
			return fmt.Errorf("outlook: add table column %s: %w", name, err)
		}
		if o, ok := v.(Object); ok {
			o.Release()
		}
	}
	return nil
}

// readRows reads a table in pages of n rows and hands each row to fn until
// the table ends or fn returns false. Every row must have one value per
// column, and its first column must spell the EntryID the table's kind
// returns (key), so a layout fault fails loudly instead of scrambling fields
// into each other.
func readRows(t Object, n, width int, key entryKey, fn func(values []any) bool) error {
	for {
		v, err := t.Call("GetArray", n)
		if err != nil {
			return err
		}
		var rows [][]any
		switch a := v.(type) {
		case nil:
		case [][]any:
			rows = a
		default:
			return fmt.Errorf("%w: %T", errLayout, v)
		}
		for _, r := range rows {
			if err := checkRow(r, width, key); err != nil {
				return err
			}
			if !fn(r) {
				return nil
			}
		}
		if len(rows) < n {
			return nil
		}
	}
}

// checkRow checks one table row's layout before it is decoded: one value per
// requested column, and an EntryID of the kind the table returns.
func checkRow(r []any, width int, key entryKey) error {
	if len(r) != width {
		return fmt.Errorf("%w: a row has %d values for %d columns", errLayout, len(r), width)
	}
	if unset(r[0]) {
		return nil
	}
	switch key {
	case binaryEntryID:
		if _, ok := r[0].([]byte); !ok {
			return fmt.Errorf("%w: column 0 holds %T, not the binary EntryID", errLayout, r[0])
		}
	case textEntryID:
		if _, ok := r[0].(string); !ok {
			return fmt.Errorf("%w: column 0 holds %T, not the text EntryID", errLayout, r[0])
		}
	}
	return nil
}
