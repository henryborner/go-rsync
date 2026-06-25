//go:build amd64

package delta

import (
	"crypto/rand"
	"fmt"
	"testing"
)

// rawChecksumSSE2 calls checksum1SSE2 and handles remainder + CHAR_OFFSET.
func rawChecksumSSE2(data []byte) (uint32, uint32) {
	var s1, s2 uint32
	if len(data) >= 32 {
		checksum1SSE2(data, &s1, &s2)
		p := len(data) - len(data)%32
		s1 += uint32(p) * CHAR_OFFSET
		s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
		for i := p; i < len(data); i++ {
			s1 += uint32(data[i]) + CHAR_OFFSET
			s2 += s1
		}
		return s1, s2
	}
	return checksum1(data)
}

// rawChecksumAVX2 calls checksum1AVX2 and handles remainder + CHAR_OFFSET.
func rawChecksumAVX2(data []byte) (uint32, uint32) {
	var s1, s2 uint32
	if len(data) >= 64 {
		checksum1AVX2(data, &s1, &s2)
		p := len(data) - len(data)%64
		s1 += uint32(p) * CHAR_OFFSET
		s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
		for i := p; i < len(data); i++ {
			s1 += uint32(data[i]) + CHAR_OFFSET
			s2 += s1
		}
		return s1, s2
	}
	return checksum1(data)
}

// rawChecksumGo uses pure Go — no SIMD dispatch.
func rawChecksumGo(data []byte) (uint32, uint32) {
	var s1, s2 uint32
	n := len(data)
	if n == 0 {
		return 0, 0
	}
	i := 0
	// 128B main loop (same as rolling_fast_amd64.go fallback)
	for i+128 <= n {
		process32 := func(off int) (g [8]uint32, sw uint32) {
			for k := 0; k < 8; k++ {
				j := off + k*4
				b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
				g[k] = b0 + b1 + b2 + b3
				sw += 4*b0 + 3*b1 + 2*b2 + b3
			}
			return
		}
		for off := 0; off < 128; off += 32 {
			g, sw := process32(off)
			s2 += 32*s1 + sw + 28*g[0] + 24*g[1] + 20*g[2] + 16*g[3] + 12*g[4] + 8*g[5] + 4*g[6] + 528*CHAR_OFFSET
			s1 += g[0] + g[1] + g[2] + g[3] + g[4] + g[5] + g[6] + g[7] + 32*CHAR_OFFSET
		}
		i += 128
	}
	// 32B tail
	for i+32 <= n {
		g, sw := func() (g [8]uint32, sw uint32) {
			for k := 0; k < 8; k++ {
				j := k * 4
				b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
				g[k] = b0 + b1 + b2 + b3
				sw += 4*b0 + 3*b1 + 2*b2 + b3
			}
			return
		}()
		s2 += 32*s1 + sw + 28*g[0] + 24*g[1] + 20*g[2] + 16*g[3] + 12*g[4] + 8*g[5] + 4*g[6] + 528*CHAR_OFFSET
		s1 += g[0] + g[1] + g[2] + g[3] + g[4] + g[5] + g[6] + g[7] + 32*CHAR_OFFSET
		i += 32
	}
	// byte-by-byte tail
	for ; i < n; i++ {
		s1 += uint32(data[i]) + CHAR_OFFSET
		s2 += s1
	}
	return s1, s2
}

func BenchmarkAllTiers(b *testing.B) {
	sizes := []int{1024, 65536, 1048576}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		label := fmt.Sprintf("%dKB", size/1024)

		b.Run("SSE2/"+label, func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				rawChecksumSSE2(data)
			}
		})
		b.Run("AVX2/"+label, func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				rawChecksumAVX2(data)
			}
		})
		b.Run("Go/"+label, func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				rawChecksumGo(data)
			}
		})
	}
}
