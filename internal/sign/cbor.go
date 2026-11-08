package sign

import (
	"encoding/binary"
	"errors"
	"math"
)

// A minimal CBOR (RFC 8949) reader for what WebAuthn registration needs: definite-length
// items only, as CTAP2 requires, with bounded depth and lengths bounded by the input.

const cborMaxDepth = 16

var errCBOR = errors.New("cbor: malformed")

type cborKind int

const (
	cborUint cborKind = iota
	cborNeg
	cborBytes
	cborText
	cborArray
	cborMap
	cborTag
	cborSimple
)

// cborItem is one decoded item. Int holds the value of uint and negative integers (as int64;
// out of range values are rejected), Bytes the content of byte and text strings, Items the
// elements of an array or the alternating keys and values of a map.
type cborItem struct {
	Kind  cborKind
	Int   int64
	Bytes []byte
	Items []cborItem
	// Simple is the simple value (20 false, 21 true, 22 null, 23 undefined) or 0xf9-0xfb for
	// a float, whose value is not kept.
	Simple byte
}

type cborReader struct {
	b   []byte
	off int
}

// decodeCBOR decodes one item from the start of b and returns it with the bytes it used.
func decodeCBOR(b []byte) (cborItem, int, error) {
	r := &cborReader{b: b}
	it, err := r.item(0)
	if err != nil {
		return cborItem{}, 0, err
	}
	return it, r.off, nil
}

func (r *cborReader) head() (major byte, info byte, arg uint64, err error) {
	if r.off >= len(r.b) {
		return 0, 0, 0, errCBOR
	}
	c := r.b[r.off]
	r.off++
	major, info = c>>5, c&0x1f
	switch {
	case info < 24:
		return major, info, uint64(info), nil
	case info <= 27:
		n := 1 << (info - 24)
		if len(r.b)-r.off < n {
			return 0, 0, 0, errCBOR
		}
		p := r.b[r.off : r.off+n]
		r.off += n
		switch n {
		case 1:
			arg = uint64(p[0])
		case 2:
			arg = uint64(binary.BigEndian.Uint16(p))
		case 4:
			arg = uint64(binary.BigEndian.Uint32(p))
		default:
			arg = binary.BigEndian.Uint64(p)
		}
		return major, info, arg, nil
	}
	return 0, 0, 0, errors.New("cbor: indefinite lengths and reserved values are not allowed")
}

func (r *cborReader) item(depth int) (cborItem, error) {
	if depth > cborMaxDepth {
		return cborItem{}, errors.New("cbor: nested too deep")
	}
	major, info, arg, err := r.head()
	if err != nil {
		return cborItem{}, err
	}
	switch major {
	case 0, 1:
		if arg > math.MaxInt64 {
			return cborItem{}, errors.New("cbor: integer out of range")
		}
		if major == 0 {
			return cborItem{Kind: cborUint, Int: int64(arg)}, nil
		}
		return cborItem{Kind: cborNeg, Int: -1 - int64(arg)}, nil
	case 2, 3:
		if arg > uint64(len(r.b)-r.off) {
			return cborItem{}, errCBOR
		}
		s := r.b[r.off : r.off+int(arg)]
		r.off += int(arg)
		k := cborBytes
		if major == 3 {
			k = cborText
		}
		return cborItem{Kind: k, Bytes: s}, nil
	case 4, 5:
		n := arg
		if major == 5 {
			if n > math.MaxUint64/2 {
				return cborItem{}, errCBOR
			}
			n *= 2
		}
		// Every item takes at least one byte: a count beyond the rest of the input is a lie.
		if n > uint64(len(r.b)-r.off) {
			return cborItem{}, errCBOR
		}
		items := make([]cborItem, 0, n)
		for range n {
			it, err := r.item(depth + 1)
			if err != nil {
				return cborItem{}, err
			}
			items = append(items, it)
		}
		k := cborArray
		if major == 5 {
			k = cborMap
		}
		return cborItem{Kind: k, Items: items}, nil
	case 6:
		inner, err := r.item(depth + 1)
		if err != nil {
			return cborItem{}, err
		}
		return cborItem{Kind: cborTag, Int: int64(min(arg, math.MaxInt64)), Items: []cborItem{inner}}, nil
	default:
		switch {
		case info < 24:
			return cborItem{Kind: cborSimple, Simple: info}, nil
		case info == 24:
			if arg < 32 {
				return cborItem{}, errCBOR
			}
			return cborItem{Kind: cborSimple, Simple: byte(arg)}, nil
		default:
			return cborItem{Kind: cborSimple, Simple: 0xe0 | info}, nil
		}
	}
}

// intKey returns the value of the map entry whose key is the integer k.
func (it cborItem) intKey(k int64) (cborItem, bool) {
	for i := 0; i+1 < len(it.Items); i += 2 {
		key := it.Items[i]
		if (key.Kind == cborUint || key.Kind == cborNeg) && key.Int == k {
			return it.Items[i+1], true
		}
	}
	return cborItem{}, false
}

// textKey returns the value of the map entry whose key is the text s.
func (it cborItem) textKey(s string) (cborItem, bool) {
	for i := 0; i+1 < len(it.Items); i += 2 {
		key := it.Items[i]
		if key.Kind == cborText && string(key.Bytes) == s {
			return it.Items[i+1], true
		}
	}
	return cborItem{}, false
}

// uniqueKeys reports whether no key of the map appears twice (keys compared as integers or
// strings; any other key kind makes the map invalid for our purposes).
func (it cborItem) uniqueKeys() bool {
	seen := map[string]bool{}
	for i := 0; i < len(it.Items); i += 2 {
		key := it.Items[i]
		var k string
		switch key.Kind {
		case cborUint, cborNeg:
			k = "i" + string(binary.BigEndian.AppendUint64(nil, uint64(key.Int)))
		case cborText:
			k = "t" + string(key.Bytes)
		default:
			return false
		}
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}
