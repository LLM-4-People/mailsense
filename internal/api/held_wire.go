package api

import "github.com/LLM-4-People/mailsense/internal/store"

// HeldFolderView names the source beside the store's folder projection, so a
// list across sources stays unambiguous. The embedded fields flatten into the
// same object: folder_id, store_id, path, held, stored, since.
type HeldFolderView struct {
	SourceID string `json:"source_id"`
	store.HeldFolder
}

// HeldRemovalsView is the one list response. Folders is always an array.
type HeldRemovalsView struct {
	Folders []HeldFolderView `json:"folders"`
}

// HeldRemovalConfirmView is the result of confirming one folder's held set.
type HeldRemovalConfirmView struct {
	Removal store.Removal `json:"removal"`
}
