//go:build amd64

package delta

import (
	"math/rand"
	"testing"

	"golang.org/x/sys/cpu"
)

// TestChecksum1AVX512Parity verifies the opt-in AVX-512 path against the
// byte-by-byte reference. Skipped when the CPU lacks AVX-512 (the ZMM
// instructions would fault).
func TestChecksum1AVX512Parity(t *testing.T) {
	if !cpu.X86.HasAVX512 {
		t.Skip("no AVX-512")
	}
	sizes := []int{64, 65, 127, 128, 700, 1024, 2048, 4096, 8192, 65536, 70000, 131072, 1048576}
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)
		wantS1, wantS2 := referenceChecksum1Raw(data)
		var s1, s2 uint32
		if !checksum1AVX512(data, &s1, &s2) {
			t.Fatalf("n=%d: avx512 refused", n)
		}
		// asm processes ALL bytes (incl. scalar remainder) and returns raw
		// sums truncated to 16 bits — compare directly.
		if (s1&0xFFFF) != (wantS1&0xFFFF) || (s2&0xFFFF) != (wantS2&0xFFFF) {
			t.Errorf("n=%d: s1 want=%d got=%d, s2 want=%d got=%d",
				n, wantS1&0xFFFF, s1&0xFFFF, wantS2&0xFFFF, s2&0xFFFF)
		}
	}
}
