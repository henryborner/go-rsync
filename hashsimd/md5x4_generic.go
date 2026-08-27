//go:build !arm64

package hashsimd

// md5x4available reports whether NEON 4-way MD5 is available.
// Stub for non-arm64 platforms.
func md5x4available() bool {
	return false
}

// md5Hash4wayNEON is a stub for non-arm64 platforms.
func md5Hash4wayNEON(data []byte, offsets [4]int, lengths [4]int, out *[4][16]byte) {
	panic("md5Hash4wayNEON called on non-arm64 platform")
}
