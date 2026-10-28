package agents

// ring keeps the last len(buf) bytes written to it.
type ring struct {
	buf  []byte
	pos  int
	full bool
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) Write(p []byte) {
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.pos, r.full = 0, true
		return
	}
	n := copy(r.buf[r.pos:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % len(r.buf)
	if r.pos == 0 && len(p) > 0 {
		r.full = true
	}
}

func (r *ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.pos:]...)
	return append(out, r.buf[:r.pos]...)
}
