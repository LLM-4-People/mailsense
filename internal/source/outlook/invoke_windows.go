//go:build windows

package outlook

import (
	"runtime"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

// invoke calls IDispatch::Invoke with arguments given as VARIANTs (in call
// order; reversed here as COM expects). go-ole's own Invoke is not used: it
// leaks a BSTR per element when passing a string array, which a full
// back-fill would do about a million times.
func invoke(d *ole.IDispatch, dispid int32, flags uint16, args []ole.VARIANT) (*ole.VARIANT, uint32, string) {
	var params dispParams
	rev := reversed(args)
	if len(rev) > 0 {
		params.args = uintptr(unsafe.Pointer(&rev[0]))
		params.count = uint32(len(rev))
	}
	result := new(ole.VARIANT)
	var ei excepInfo
	hr, _, _ := syscall.SyscallN(d.VTable().Invoke, uintptr(unsafe.Pointer(d)), uintptr(dispid),
		uintptr(unsafe.Pointer(ole.IID_NULL)), uintptr(ole.GetUserDefaultLCID()), uintptr(flags),
		uintptr(unsafe.Pointer(&params)), uintptr(unsafe.Pointer(result)), uintptr(unsafe.Pointer(&ei)), 0)
	// params holds rev only as a uintptr: keep the arguments alive (and
	// unmoved) until Invoke returned (review 1b #6).
	runtime.KeepAlive(rev)
	if uint32(hr) == 0 {
		return result, 0, ""
	}
	code, desc := uint32(hr), ""
	if code == hrException {
		if ei.scode != 0 {
			code = ei.scode
		}
		desc = ei.take()
	}
	return nil, code, desc
}

// dispParams is DISPPARAMS (no named arguments: the connector never puts).
type dispParams struct {
	args       uintptr
	namedArgs  uintptr
	count      uint32
	namedCount uint32
}

// excepInfo is EXCEPINFO.
type excepInfo struct {
	wCode             uint16
	wReserved         uint16
	bstrSource        *uint16
	bstrDescription   *uint16
	bstrHelpFile      *uint16
	dwHelpContext     uint32
	pvReserved        uintptr
	pfnDeferredFillIn uintptr
	scode             uint32
}

// take returns the description and frees the strings Outlook allocated.
func (e *excepInfo) take() string {
	desc := ""
	if e.bstrDescription != nil {
		desc = ole.BstrToString(e.bstrDescription)
	}
	for _, p := range []*uint16{e.bstrSource, e.bstrDescription, e.bstrHelpFile} {
		if p != nil {
			_ = ole.SysFreeString((*int16)(unsafe.Pointer(p)))
		}
	}
	return desc
}
