package hashsimd

import (
	"crypto/md5"
	"testing"
)

// md5Hash4wayGoLogic validates the 4-way MD5 reference algorithm
// against crypto/md5 on any platform (no ARM64 required).
func TestMD5x4_GoRef_Logic(t *testing.T) {
	sizes := []int{64, 128, 255, 700}
	data := make([]byte, 4*700)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [4]int
	off := 0
	for b := 0; b < 4; b++ {
		offsets[b] = off
		lengths[b] = sizes[b]
		off += 700
	}

	var out [4][16]byte
	md5Hash4wayGo(data, offsets, lengths, &out)

	for b := 0; b < 4; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d (len=%d) mismatch:\n  got:  %x\n  want: %x",
				b, lengths[b], out[b], expected)
		}
	}
}
