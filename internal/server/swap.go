package server

import (
	"net/http"
	"sync/atomic"
)

// SwapHandler is an http.Handler whose downstream target can be
// replaced atomically. It exists for exactly one use case: letting
// TLSManager.Enable wrap the plain-HTTP listener's handler with ACME
// challenge passthrough + redirect-to-HTTPS, without restarting the
// listener (which would drop the wizard session that just clicked
// "Enable HTTPS").
//
// The swap is a single store on a pointer; the request path is one
// atomic load and one dereference.
//
// The zero value is not usable — construct via NewSwapHandler so the
// first request finds a non-nil holder.
type SwapHandler struct {
	cur atomic.Pointer[handlerHolder]
}

type handlerHolder struct{ h http.Handler }

// NewSwapHandler returns a SwapHandler initially serving h. h must be
// non-nil.
func NewSwapHandler(h http.Handler) *SwapHandler {
	s := &SwapHandler{}
	s.Set(h)
	return s
}

// Set replaces the downstream handler. Safe to call concurrently with
// ServeHTTP; in-flight requests continue serving from whichever handler
// they loaded. Panics on nil so a programmer error fails at the call
// site instead of on the next request.
func (s *SwapHandler) Set(h http.Handler) {
	if h == nil {
		panic("server: SwapHandler.Set(nil)")
	}
	s.cur.Store(&handlerHolder{h: h})
}

func (s *SwapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.cur.Load().h.ServeHTTP(w, r)
}
