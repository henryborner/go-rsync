//go:build amd64

package delta

import "golang.org/x/sys/cpu"

// checksum1AVX512 is the opt-in AVX-512 single-ZMM rolling checksum
// (64 B/iter, ~11 insns/loop, 16-bit-lane). Returns raw sums truncated to
// 16 bits. Must only be called when cpu.X86.HasAVX512 is true, otherwise the
// ZMM instructions fault (SIGILL).
func checksum1AVX512(data []byte, s1, s2 *uint32) bool

// Checksum1AVX512 computes the rolling checksum on the AVX-512 path.
//
// This is an explicit opt-in: Checksum1 auto-dispatches (AVX2 → SSE2 → Go)
// and is the right choice on most machines. The single-ZMM 64 B/iter loop is
// measurably faster than AVX2 only on Intel server Xeons with full-width
// 512-bit integer units, and only for blocks ≥ 16 KB (up to +27% at 256 KB);
// on AMD Zen 4 it is slower, and on CPUs without AVX-512 it would crash.
// See docs/benchmarks.md → "AVX-512 rolling checksum experiment".
//
// ⚠️ Not guaranteed faster on all Intel CPUs — measured on one Cascade Lake
// Xeon only; benchmark on your own hardware before enabling.
//
// Falls back to Checksum1 when the CPU lacks AVX-512 or the asm refuses.
func Checksum1AVX512(data []byte) uint32 {
	n := len(data)
	if !cpu.X86.HasAVX512 || n < 64 {
		return Checksum1(data)
	}
	var s1, s2 uint32
	if !checksum1AVX512(data, &s1, &s2) {
		return Checksum1(data)
	}
	s1 += uint32(n) * CHAR_OFFSET
	s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET
	return (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)
}
