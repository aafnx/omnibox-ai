//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protect encrypts data with DPAPI for this computer (LocalMachine): only a process on this PC
// can read it back, so a copied file is useless elsewhere.
func Protect(data []byte) ([]byte, error) {
	return dpapi(data, true)
}

// Unprotect reverses Protect.
func Unprotect(data []byte) ([]byte, error) {
	return dpapi(data, false)
}

func dpapi(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, windows.ERROR_INVALID_DATA
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN|windows.CRYPTPROTECT_LOCAL_MACHINE, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
