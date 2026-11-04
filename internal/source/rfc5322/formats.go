package rfc5322

import (
	_ "embed"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

//go:embed formats.json
var formatsJSON []byte

// fileFormats is formats.json (TestFormatsAreValid guards it; a broken
// file is a build defect).
type fileFormats struct {
	status map[string]bool // lower-case header names
	labels map[string]mail.FolderRole
	unread string // the Gmail label of an unread message
}

var formats = sync.OnceValue(func() fileFormats {
	var doc struct {
		Doc    string                     `json:"_doc"`
		Status []string                   `json:"status_headers"`
		Labels map[string]mail.FolderRole `json:"gmail_label_roles"`
		Unread string                     `json:"gmail_unread_label"`
	}
	if err := jsonx.DecodeStrict(formatsJSON, &doc); err != nil {
		panic("rfc5322: formats.json: " + err.Error())
	}
	out := fileFormats{status: map[string]bool{}, labels: doc.Labels, unread: doc.Unread}
	for _, h := range doc.Status {
		out.status[strings.ToLower(h)] = true
	}
	return out
})
