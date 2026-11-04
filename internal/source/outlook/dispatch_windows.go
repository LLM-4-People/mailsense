//go:build windows

package outlook

import (
	ole "github.com/go-ole/go-ole"
)

// dispatch is an Object backed by a COM IDispatch. It must only be used on
// the apartment thread that created it.
type dispatch struct {
	d     *ole.IDispatch
	ids   *dispidCache // shared per session: one GetIDsOfNames per class and member
	class string
	own   map[string]int32 // per-object cache for classes that are not cacheable
}

var _ classed = (*dispatch)(nil)

func (o *dispatch) SetClass(class string) { o.class = class }

func (o *dispatch) Release() {
	if o.d != nil {
		o.d.Release()
		o.d = nil
	}
}

func (o *dispatch) Get(name string, args ...any) (any, error) {
	return o.invoke(name, ole.DISPATCH_PROPERTYGET, args)
}

func (o *dispatch) Call(name string, args ...any) (any, error) {
	return o.invoke(name, ole.DISPATCH_METHOD, args)
}

func (o *dispatch) Put(name string, value any) error {
	if o.d == nil {
		return &ComError{Code: hrObjNotConnected, Member: name, Desc: "object already released"}
	}
	res, err := o.d.PutProperty(name, value)
	if res != nil {
		_ = ole.VariantClear(res)
	}
	if err != nil {
		return comError(name, err)
	}
	return nil
}

func (o *dispatch) dispid(name string) (int32, error) {
	return o.ids.id(o.class, &o.own, name, func() (int32, error) {
		ids, err := o.d.GetIDsOfName([]string{name})
		if err != nil {
			return 0, comError(name, err)
		}
		return ids[0], nil
	})
}

func (o *dispatch) invoke(name string, flags uint16, args []any) (any, error) {
	if o.d == nil {
		return nil, &ComError{Code: hrObjNotConnected, Member: name, Desc: "object already released"}
	}
	id, err := o.dispid(name)
	if err != nil {
		return nil, err
	}
	vargs := make([]ole.VARIANT, 0, len(args))
	defer func() {
		for i := range vargs {
			_ = ole.VariantClear(&vargs[i])
		}
	}()
	for _, a := range args {
		v, err := toVariant(a)
		if err != nil {
			return nil, err
		}
		vargs = append(vargs, v)
	}
	res, code, desc := invoke(o.d, id, flags, vargs)
	if code != 0 {
		return nil, &ComError{Code: code, Member: name, Desc: desc}
	}
	v, owned, err := fromVariant(res, func(d *ole.IDispatch) Object { return &dispatch{d: d, ids: o.ids} })
	if !owned {
		_ = ole.VariantClear(res)
	}
	return v, err
}

// comError converts a go-ole error.
func comError(member string, err error) error {
	if oe, ok := err.(*ole.OleError); ok {
		return &ComError{Code: uint32(oe.Code()), Member: member, Desc: oe.Description()}
	}
	return err
}
