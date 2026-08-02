//go:build amd64

package delta

import (
	"crypto/rand"
	"testing"
)

func TestAVX2Parity(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"zeros-64", make([]byte, 64)},
		{"zeros-128", make([]byte, 128)},
		{"ones-64", bytesRepeat(64, 0xFF)},
		{"ones-128", bytesRepeat(128, 0xFF)},
		{"ones-256", bytesRepeat(256, 0xFF)},
		{"inc-64", incBytes(64)},
		{"inc-128", incBytes(128)},
		{"inc-200", incBytes(200)},
		{"rand-128", randBytes(128)},
		{"rand-700", randBytes(700)},
		{"rand-2048", randBytes(2048)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantS1, wantS2 := referenceChecksum1Raw(tt.data)
			var avxS1, avxS2 uint32
			if !checksum1AVX2(tt.data, &avxS1, &avxS2) {
				t.Fatal("AVX2 refused")
			}
			// asm now processes ALL bytes (64B blocks + scalar remainder) and
			// returns raw sums truncated to 16 bits (16-bit-lane design).
			if avxS1 != (wantS1 & 0xFFFF) || avxS2 != (wantS2 & 0xFFFF) {
				t.Errorf("%s: len=%d s1 want=%d got=%d, s2 want=%d got=%d",
					tt.name, len(tt.data), wantS1&0xFFFF, avxS1, wantS2&0xFFFF, avxS2)
			}
		})
	}
}

func TestSSE2Parity(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"zeros-32", make([]byte, 32)},
		{"zeros-64", make([]byte, 64)},
		{"ones-32", bytesRepeat(32, 0xFF)},
		{"ones-64", bytesRepeat(64, 0xFF)},
		{"ones-128", bytesRepeat(128, 0xFF)},
		{"inc-32", incBytes(32)},
		{"inc-64", incBytes(64)},
		{"inc-100", incBytes(100)},
		{"rand-64", randBytes(64)},
		{"rand-700", randBytes(700)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantS1, wantS2 := referenceChecksum1Raw(tt.data)
			var sseS1, sseS2 uint32
			if !checksum1SSE2(tt.data, &sseS1, &sseS2) {
				t.Fatal("SSE2 refused")
			}
			p := len(tt.data) - len(tt.data)%32
			for i := p; i < len(tt.data); i++ {
				sseS1 += uint32(tt.data[i])
				sseS2 += sseS1
			}
			// asm returns raw sums truncated to 16 bits (16-bit-lane design);
			// the Go tail loop above runs in full uint32, so mask both sides.
			if (sseS1 & 0xFFFF) != (wantS1 & 0xFFFF) || (sseS2 & 0xFFFF) != (wantS2 & 0xFFFF) {
				t.Errorf("%s: len=%d s1 want=%d got=%d, s2 want=%d got=%d",
					tt.name, len(tt.data), wantS1&0xFFFF, sseS1&0xFFFF, wantS2&0xFFFF, sseS2&0xFFFF)
			}
		})
	}
}

func bytesRepeat(n int, b byte) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = b
	}
	return d
}

func incBytes(n int) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i)
	}
	return d
}

func randBytes(n int) []byte {
	d := make([]byte, n)
	rand.Read(d)
	return d
}

// referenceChecksum1Raw is byte-by-byte without CHAR_OFFSET.
func referenceChecksum1Raw(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b)
		s2 += s1
	}
	return
}
