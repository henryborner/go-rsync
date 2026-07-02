//go:build !amd64

package delta

// md5x16available reports whether AVX-512 16-way MD5 is available.
// Stub for non-amd64 platforms.
func md5x16available() bool {
	return false
}

// MD5x16available is the exported version for external use.
func MD5x16available() bool { return false }

// md5Hash16wayAVX512 is a stub for non-amd64 platforms.
func md5Hash16wayAVX512(data []byte, offsets [16]int, lengths [16]int, out *[16][16]byte) {
	panic("md5Hash16wayAVX512 called on non-amd64 platform")
}
