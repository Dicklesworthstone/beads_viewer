//go:build !race
// +build !race

package decoder

import (
	"sync"
	"unsafe"

	"github.com/goccy/go-json/internal/runtime"
)

// Decoder interfaces contain more than one word. Publish them under the same
// lock used by the race build so concurrent first use cannot read a partial
// interface (for example, a *structDecoder with a nil data pointer).
var decMu sync.RWMutex

func CompileToGetDecoder(typ *runtime.Type) (Decoder, error) {
	initDecoder()
	typeptr := uintptr(unsafe.Pointer(typ))
	if typeptr > typeAddr.MaxTypeAddr {
		return compileToGetDecoderSlowPath(typeptr, typ)
	}

	index := (typeptr - typeAddr.BaseTypeAddr) >> typeAddr.AddrShift
	decMu.RLock()
	if dec := cachedDecoder[index]; dec != nil {
		decMu.RUnlock()
		return dec, nil
	}
	decMu.RUnlock()

	dec, err := compileHead(typ, map[uintptr]Decoder{})
	if err != nil {
		return nil, err
	}
	decMu.Lock()
	cachedDecoder[index] = dec
	decMu.Unlock()
	return dec, nil
}
