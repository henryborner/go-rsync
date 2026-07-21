//go:build !amd64

package delta

// sha256x8available reports whether AVX2 SHA-256 8-way is available.
// Stub for non-amd64 platforms.
func sha256x8available() bool {
	return false
}

// SHA256x8Available is the exported version for external use.
func SHA256x8Available() bool { return false }

// sha256Hash8wayAVX2 is a stub for non-amd64 platforms.
func sha256Hash8wayAVX2(data []byte, offsets [8]int, lengths [8]int, out *[8][32]byte) {
	panic("sha256Hash8wayAVX2 called on non-amd64 platform")
}

// Reference sha256FinalLane so it's not flagged as unused on non-amd64.
var _ = sha256FinalLane
