package keystore

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileProtector seals the file backend's contents with the Windows Data
// Protection API, so the bytes on disk are readable only by this user account
// on this machine. The file's own permissions are still set to owner-only:
// DPAPI is the confidentiality, permissions are the first line.
//
// A passphrase, if the caller supplied one, is ignored here on purpose. DPAPI
// is bound to the logged-in account and is strictly stronger than a passphrase
// the client would have to prompt for at every launch.
func fileProtector(opts Options) (protector, error) {
	p := dpapiProtector{}
	_, err := p.protect([]byte("probe"))
	if err == nil {
		return p, nil
	}
	// DPAPI can refuse in an unusual logon context, and a client that cannot
	// store its device key cannot run at all. Falling back keeps it running on
	// the portable protection; Store.Backend() reports which one is in use.
	//nolint:nilerr // a failed probe is the reason to fall back, not an error to report
	return defaultFileProtector(opts), nil
}

var (
	crypt32            = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtect   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32.NewProc("CryptUnprotectData")
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procLocalFree      = kernel32.NewProc("LocalFree")
)

// cryptProtectUIForbidden fails the call rather than showing a dialog: this
// runs inside a background tray service, where a modal prompt would be
// invisible (SPEC §7.2).
const cryptProtectUIForbidden = 0x1

// dataBlob is DATA_BLOB from wincrypt.h.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// bytes copies the blob's contents out of the memory CryptoAPI allocated.
func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(b.pbData, b.cbData)...)
}

func (b dataBlob) free() {
	if b.pbData != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	}
}

type dpapiProtector struct{}

func (dpapiProtector) backend() Backend { return BackendDPAPI }

func (dpapiProtector) protect(plaintext []byte) ([]byte, error) {
	in := newBlob(plaintext)
	var out dataBlob
	ret, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer out.free()
	return out.bytes(), nil
}

func (dpapiProtector) open(sealed []byte) ([]byte, error) {
	in := newBlob(sealed)
	var out dataBlob
	ret, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		// A file from another account or another machine lands here, as does
		// a tampered one.
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer out.free()
	return out.bytes(), nil
}
