package hashsimd

import (
	"crypto/md5"
	"crypto/rand"
	"testing"
)

// FuzzMD5x8Parity verifies the 8-way AVX2 path against crypto/md5 and the
// pure-Go reference on fuzzed inputs. Moved here from go-rsync core when the
// SIMD engine was split into its own module.
func FuzzMD5x8Parity(t *testing.F) {
	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	seeds := []int{64, 128, 700, 1024, 2048}
	for _, sz := range seeds {
		data := make([]byte, 8*sz)
		rand.Read(data)
		t.Add(data, sz)
	}

	t.Fuzz(func(t *testing.T, data []byte, blockLen int) {
		if blockLen < 1 || blockLen > 8192 {
			return
		}
		need := 8 * blockLen
		if len(data) < need {
			return
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * blockLen
			lengths[b] = blockLen
		}

		var outSIMD [8][16]byte
		MD5Hash8way(data, offsets, lengths, &outSIMD)

		for b := 0; b < 8; b++ {
			expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
			if outSIMD[b] != expected {
				t.Fatalf("lane %d (len=%d) mismatch:\n  SIMD: %x\n  md5:  %x",
					b, blockLen, outSIMD[b], expected)
			}
		}

		// Also compare against pure Go reference.
		var outGo [8][16]byte
		md5Hash8wayGo(data, offsets, lengths, &outGo)
		for b := 0; b < 8; b++ {
			if outSIMD[b] != outGo[b] {
				t.Fatalf("lane %d (len=%d) SIMD vs Go:\n  SIMD: %x\n  Go:   %x",
					b, blockLen, outSIMD[b], outGo[b])
			}
		}
	})
}
