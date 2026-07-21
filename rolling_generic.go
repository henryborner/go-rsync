//go:build !amd64 && !arm64

package delta

// checksum1 is the original byte-by-byte checksum for non-amd64 platforms.
func checksum1(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b) + CHAR_OFFSET
		s2 += s1
	}
	return
}

// Checksum1 computes a one-shot rolling checksum.
func Checksum1(data []byte) uint32 {
	if len(data) == 0 {
		return 0
	}
	s1, s2 := checksum1(data)
	return (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)
}

// Checksum1Components returns the raw (s1, s2) components.
func Checksum1Components(data []byte) (s1, s2 uint32) {
	return checksum1(data)
}
