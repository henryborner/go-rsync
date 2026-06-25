//go:build !amd64

package delta

// checksum1 is the original byte-by-byte checksum for non-amd64 platforms.
func checksum1(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b) + CHAR_OFFSET
		s2 += s1
	}
	return
}
