//go:build !amd64

package delta

// md5x8available reports whether AVX2 8-way MD5 is available.
// Stub for non-amd64 platforms.
func md5x8available() bool {
	return false
}

// md5Hash8wayAVX2 is a stub for non-amd64 platforms.
func md5Hash8wayAVX2(data []byte, offsets [8]int, lengths [8]int, out *[8][16]byte) {
	panic("md5Hash8wayAVX2 called on non-amd64 platform")
}
