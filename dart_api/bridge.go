//go:build !js

package dart_api

/*
#include <stdint.h>
#include <stdlib.h>
#include "dart_api_dl.h"
#include "bridge.h"
typedef struct bytesContainer
{
    void *message;
    int size;
} BytesContainer;
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func InitializeDartAPI(api unsafe.Pointer) C.int64_t {
	return C.int64_t(C.Dart_InitializeApiDL(api))
}

func SendPointerAddress(port, ptrAddr int64) error {
	if ok := C.GoDart_PostPointerAddress(C.Dart_Port_DL(port), C.int64_t(ptrAddr)); !ok {
		return fmt.Errorf("failed to send a pointer address to Dart_Port(%d): %d", port, ptrAddr)
	}
	return nil
}

func PointerAddr(bc unsafe.Pointer) C.int64_t {
	return C.PointerAddr(bc)
}

// BytesToContainer allocates a BytesContainer on the C heap holding a copy of
// b and returns the container pointer. Used by the async response path via
// [BytesToPointerAddress], where the container address is posted to a Dart port.
func BytesToContainer(b []byte) unsafe.Pointer {
	bc := (*C.BytesContainer)(C.malloc(C.size_t(C.sizeof_BytesContainer)))
	bc.message = C.CBytes(b)
	bc.size = C.int(len(b))
	return unsafe.Pointer(bc)
}

// BytesToPointerAddress is the async-path variant of BytesToContainer: it
// returns the container address as an int64 suitable for GoDart_PostPointerAddress.
//
// Ownership: the container and its message buffer are allocated here with
// the C allocator and must be freed *through the Go-side free path*
// (Dart calls the exported `FreeBytesContainer` symbol, which runs
// GoDash_FreeBytesContainer in bridge.c) — never with Dart's malloc.free.
func BytesToPointerAddress(b []byte) int64 {
	return int64(PointerAddr(BytesToContainer(b)))
}

// Don't close the port in Go when the port is created in Dart.
func ClosePort(port int64) error {
	if ok := C.GoDart_CloseNativePort(C.Dart_Port_DL(port)); !ok {
		return fmt.Errorf("failed to close a Dart_Port(%d)", port)
	}
	return nil
}
