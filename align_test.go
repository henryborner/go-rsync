//go:build arm64

package delta

import (
	"crypto/rand"
	mrand "math/rand"
	"fmt"
	"testing"
)

// ── Raw parity (NEON asm vs byte-by-byte reference, no CHAR_OFFSET) ──

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
			checkRawParity(t, tt.data)
		})
	}
}

// ── Edge-case sizes ──

func TestNEONEdgeCases(t *testing.T) {
	sizes := []int{0, 1, 31, 63, 64, 65, 127, 128, 129, 255, 256, 257, 511, 1023, 1024, 1025, 4095, 4096}
	for _, n := range sizes {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte((i * 37) ^ (i >> 3)) // pseudo-random pattern, deterministic
		}
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			checkRawParity(t, data)
		})
	}
}

// ── Random data fuzz ──

func TestNEONRandom(t *testing.T) {
	for i := 0; i < 50; i++ {
		n := 64 + mrand.Intn(10000)
		data := make([]byte, n)
		rand.Read(data)
		t.Run(fmt.Sprintf("rand-%d", n), func(t *testing.T) {
			checkRawParity(t, data)
		})
	}
}

// ── CHAR_OFFSET parity (public Checksum1 vs pure Go reference) ──

func TestChecksum1ParityAmd64Style(t *testing.T) {
	sizes := []int{512, 1024, 4096, 8192, 16384, 32768, 65535, 65536, 65537, 70000, 92681, 100000, 128 * 1024}
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			got := Checksum1(data)
			want := checksum1PureGo(data)
			if got != want {
				t.Errorf("Checksum1(%d): got=%08x want=%08x", n, got, want)
			}
		})
	}
}

// ── Stress test: large random buffer ──

func TestNEONLargeRandom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large test in short mode")
	}
	sizes := []int{256 * 1024, 1024 * 1024, 4 * 1024 * 1024}
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)
		t.Run(fmt.Sprintf("%dMB", n/(1024*1024)), func(t *testing.T) {
			checkRawParity(t, data)
		})
	}
}

// ── Helpers ──

func checkRawParity(t *testing.T, data []byte) {
	t.Helper()
	wantS1, wantS2 := referenceChecksum1Raw(data)
	var neonS1, neonS2 uint32
	p := 0
	if checksum1NEON(data, &neonS1, &neonS2) {
		p = len(data) - len(data)%64
	} else {
		neonS1, neonS2 = 0, 0
	}
	for i := p; i < len(data); i++ {
		neonS1 += uint32(data[i])
		neonS2 += neonS1
	}
	if neonS1 != wantS1 || neonS2 != wantS2 {
		t.Errorf("len=%d s1 want=%d got=%d, s2 want=%d got=%d",
			len(data), wantS1, neonS1, wantS2, neonS2)
	}
}

func checksum1PureGo(data []byte) uint32 {
	var s1, s2 uint32
	for _, b := range data {
		s1 += uint32(b) + CHAR_OFFSET
		s2 += s1
	}
	return (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)
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
