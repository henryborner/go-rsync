//go:build amd64

package delta

// checksum1SSE2 is the SSE2/SSSE3 checksum (32B/iter, XMM registers).
// Declared in rolling_sse2_amd64.s.
func checksum1SSE2(data []byte, s1, s2 *uint32) bool
