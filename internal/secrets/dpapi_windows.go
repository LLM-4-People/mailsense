//go:build windows

package secrets

import (
	"unsafe"

	"github.com/LLM-4-People/mailsense/internal/fsx"

	"golang.org/x/sys/windows"
)

// openPlatform: the DPAPI file store (current-user scope, no UI prompts).
func openPlatform(path string, _ []string, policy func() fsx.Replace) (Store, error) {
	return openFileStore(path, dpapi{}, policy)
}

type dpapi struct{}

func (dpapi) protect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptProtectData(blob(plain), nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return take(&out), nil
}

func (dpapi) unprotect(sealed []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(sealed), nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return take(&out), nil
}

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// take copies a DPAPI-allocated buffer into Go memory and frees it.
func take(b *windows.DataBlob) []byte {
	if b.Data == nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(b.Data)))
	return append([]byte(nil), unsafe.Slice(b.Data, b.Size)...)
}
