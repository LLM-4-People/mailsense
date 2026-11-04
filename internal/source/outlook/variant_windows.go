//go:build windows

package outlook

import (
	"fmt"
	"math"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// SAFEARRAY functions go-ole does not export. The DLL handle is immutable
// once loaded.
var (
	oleaut32             = windows.NewLazySystemDLL("oleaut32.dll")
	procSafeArrayGetDim  = oleaut32.NewProc("SafeArrayGetDim")
	procSafeArrayGetLB   = oleaut32.NewProc("SafeArrayGetLBound")
	procSafeArrayGetUB   = oleaut32.NewProc("SafeArrayGetUBound")
	procSafeArrayAccess  = oleaut32.NewProc("SafeArrayAccessData")
	procSafeArrayUnacc   = oleaut32.NewProc("SafeArrayUnaccessData")
	procSafeArrayElem    = oleaut32.NewProc("SafeArrayGetElemsize")
	procSafeArrayCreateV = oleaut32.NewProc("SafeArrayCreateVector")
)

// toVariant converts one argument. The caller clears it after the call.
func toVariant(a any) (ole.VARIANT, error) {
	switch v := a.(type) {
	case string:
		return bstr(v), nil
	case int:
		return long(int64(v))
	case int64:
		return long(v)
	case bool:
		if v {
			return ole.NewVariant(ole.VT_BOOL, 0xffff), nil
		}
		return ole.NewVariant(ole.VT_BOOL, 0), nil
	case []string:
		return stringArray(v)
	case Date:
		return ole.NewVariant(ole.VT_DATE, int64(math.Float64bits(float64(v)))), nil
	}
	return ole.VARIANT{}, fmt.Errorf("outlook: unsupported COM argument %T", a)
}

func long(v int64) (ole.VARIANT, error) {
	n, err := int32Arg(v)
	return ole.NewVariant(ole.VT_I4, int64(n)), err
}

func bstr(s string) ole.VARIANT {
	return ole.NewVariant(ole.VT_BSTR, int64(uintptr(unsafe.Pointer(ole.SysAllocStringLen(s)))))
}

// stringArray builds a 1-D SAFEARRAY of VARIANT(BSTR), the shape
// PropertyAccessor.GetProperties takes (what a VBA/PowerShell array of
// strings marshals to). Clearing the VARIANT frees the array and its strings.
func stringArray(ss []string) (ole.VARIANT, error) {
	psa, _, _ := procSafeArrayCreateV.Call(uintptr(ole.VT_VARIANT), 0, uintptr(len(ss)))
	if psa == 0 {
		return ole.VARIANT{}, fmt.Errorf("outlook: SafeArrayCreateVector failed")
	}
	var data unsafe.Pointer
	if hr, _, _ := procSafeArrayAccess.Call(psa, uintptr(unsafe.Pointer(&data))); hr != 0 {
		return ole.VARIANT{}, fmt.Errorf("outlook: SafeArrayAccessData: 0x%08X", uint32(hr))
	}
	elems := unsafe.Slice((*ole.VARIANT)(data), len(ss))
	for i, s := range ss {
		elems[i] = bstr(s)
	}
	procSafeArrayUnacc.Call(psa)
	return ole.NewVariant(ole.VT_ARRAY|ole.VT_VARIANT, int64(psa)), nil
}

// fromVariant converts a result. VT_DISPATCH becomes an Object that owns
// the reference (the caller must not clear v then); everything else is
// copied and v must be cleared by the caller.
func fromVariant(v *ole.VARIANT, wrap func(*ole.IDispatch) Object) (any, bool, error) {
	if v.VT&ole.VT_ARRAY != 0 {
		a, err := readArray(uintptr(v.Val), v.VT&^ole.VT_ARRAY)
		return a, false, err
	}
	if v.VT == ole.VT_DISPATCH {
		d := *(**ole.IDispatch)(unsafe.Pointer(&v.Val))
		if d == nil {
			return nil, false, nil
		}
		return wrap(d), true, nil
	}
	s, err := scalar(v)
	return s, false, err
}

// scalar converts a non-array, non-object VARIANT.
func scalar(v *ole.VARIANT) (any, error) {
	switch v.VT {
	case ole.VT_EMPTY, ole.VT_NULL:
		return nil, nil
	case ole.VT_BSTR:
		return ole.BstrToString(*(**uint16)(unsafe.Pointer(&v.Val))), nil
	case ole.VT_I1:
		return int64(int8(v.Val)), nil
	case ole.VT_I2:
		return int64(int16(v.Val)), nil
	case ole.VT_I4, ole.VT_INT:
		return int64(int32(v.Val)), nil
	case ole.VT_I8:
		return v.Val, nil
	case ole.VT_UI1:
		return int64(uint8(v.Val)), nil
	case ole.VT_UI2:
		return int64(uint16(v.Val)), nil
	case ole.VT_UI4, ole.VT_UINT:
		return int64(uint32(v.Val)), nil
	case ole.VT_UI8:
		return int64(uint64(v.Val)), nil
	case ole.VT_BOOL:
		return v.Val&0xffff != 0, nil
	case ole.VT_R4:
		return float64(math.Float32frombits(uint32(v.Val))), nil
	case ole.VT_R8:
		return math.Float64frombits(uint64(v.Val)), nil
	case ole.VT_DATE:
		return Date(math.Float64frombits(uint64(v.Val))), nil
	case ole.VT_ERROR:
		return PropError(uint32(v.Val)), nil
	}
	return nil, fmt.Errorf("outlook: unsupported COM value type %d", v.VT)
}

// readArray copies a SAFEARRAY: VT_UI1 → []byte; VARIANT or BSTR elements
// → []any (1-D) or [][]any (2-D, columnMajor: dimension 1 is the row, as
// verified live on 1,262 GetArray pages).
// The connector checks every row's width and its binary EntryID column, so
// a wrong assumption here fails loudly (errLayout) instead of scrambling.
func readArray(psa uintptr, elem ole.VT) (any, error) {
	if psa == 0 {
		return nil, nil
	}
	n, size, err := arrayShape(psa)
	if err != nil {
		return nil, err
	}
	var data unsafe.Pointer
	if hr, _, _ := procSafeArrayAccess.Call(psa, uintptr(unsafe.Pointer(&data))); hr != 0 {
		return nil, fmt.Errorf("outlook: SafeArrayAccessData: 0x%08X", uint32(hr))
	}
	defer procSafeArrayUnacc.Call(psa)
	total := n[0]
	if len(n) == 2 {
		total *= n[1]
	}
	if elem == ole.VT_UI1 && len(n) == 1 {
		return append([]byte(nil), unsafe.Slice((*byte)(data), total)...), nil
	}
	at := func(i int) (any, error) { return element(unsafe.Add(data, uintptr(i)*size), elem) }
	if len(n) == 2 {
		return columnMajor(n[0], n[1], at)
	}
	out := make([]any, total)
	for i := range out {
		if out[i], err = at(i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// arrayShape returns the element count of each dimension (1 or 2) and the
// element size of a SAFEARRAY.
func arrayShape(psa uintptr) ([]int, uintptr, error) {
	dims, _, _ := procSafeArrayGetDim.Call(psa)
	if dims < 1 || dims > 2 {
		return nil, 0, fmt.Errorf("outlook: %d-dimensional array not supported", dims)
	}
	n := make([]int, dims)
	for d := range n {
		var lb, ub int32
		procSafeArrayGetLB.Call(psa, uintptr(d+1), uintptr(unsafe.Pointer(&lb)))
		procSafeArrayGetUB.Call(psa, uintptr(d+1), uintptr(unsafe.Pointer(&ub)))
		n[d] = max(int(ub-lb+1), 0)
	}
	size, _, _ := procSafeArrayElem.Call(psa)
	return n, size, nil
}

// element converts one array element in place (the array owns it).
func element(p unsafe.Pointer, elem ole.VT) (any, error) {
	switch elem {
	case ole.VT_VARIANT:
		ev := (*ole.VARIANT)(p)
		if ev.VT&ole.VT_ARRAY != 0 {
			return readArray(uintptr(ev.Val), ev.VT&^ole.VT_ARRAY)
		}
		return scalar(ev)
	case ole.VT_BSTR:
		return ole.BstrToString(*(**uint16)(p)), nil
	}
	return nil, fmt.Errorf("outlook: unsupported array element type %d", elem)
}
