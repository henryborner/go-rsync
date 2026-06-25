//go:build amd64

package delta

// checksum1AVX2 is implemented in rolling_amd64.s (AVX2 assembly).
//go:noescape
func checksum1AVX2(data []byte, s1, s2 *uint32) bool
