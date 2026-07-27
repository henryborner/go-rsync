//go:build arm64

package delta

import (
	"crypto/rand"
	"fmt"
	"testing"
)

func TestNEONParityRaw(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"zeros-64", make([]byte, 64)},
		{"zeros-128", make([]byte, 128)},
		{"ones-64", bytesRepeat(64, 0xFF)},
		{"ones-128", bytesRepeat(128, 0xFF)},
		{"inc-64", incBytes(64)},
		{"inc-96", incBytes(96)},
		{"inc-128", incBytes(128)},
		{"inc-160", incBytes(160)},
		{"inc-200", incBytes(200)},
		{"inc-256", incBytes(256)},
		{"inc-1024", incBytes(1024)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantS1, wantS2 := referenceChecksum1Raw(tt.data)
			var neonS1, neonS2 uint32
			p := 0
			if checksum1NEON(tt.data, &neonS1, &neonS2) {
				p = len(tt.data) - len(tt.data)%64
			} else {
				// below 128B threshold, use pure Go
				neonS1, neonS2 = 0, 0
			}
			for i := p; i < len(tt.data); i++ {
				neonS1 += uint32(tt.data[i])
				neonS2 += neonS1
			}
			if neonS1 != wantS1 || neonS2 != wantS2 {
				t.Errorf("%s: len=%d s1 want=%d got=%d, s2 want=%d got=%d",
					tt.name, len(tt.data), wantS1, neonS1, wantS2, neonS2)
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

// referenceChecksum1Raw is byte-by-byte without CHAR_OFFSET.
func referenceChecksum1Raw(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b)
		s2 += s1
	}
	return
}

// BenchmarkChecksum1PureGo measures the byte-by-byte Go reference.
func BenchmarkChecksum1PureGo(b *testing.B) {
	sizes := []int{1024, 8192, 65536, 1048576}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		charOffset := uint32(CHAR_OFFSET)
		b.Run(fmt.Sprintf("%dKB", size/1024), func(b *testing.B) {
			b.SetBytes(int64(size))
			for b.Loop() {
				var s1, s2 uint32
				for _, v := range data {
					s1 += uint32(v) + charOffset
					s2 += s1
				}
				_ = (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)
			}
		})
	}
}
