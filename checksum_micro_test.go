//go:build amd64

package delta

import (
	"crypto/rand"
	"fmt"
	"testing"
)

// BenchmarkChecksumMicro isolates two layers:
//   Raw:   checksum1AVX2 asm only (raw byte sums truncated to 16 bits, no CHAR_OFFSET)
//   Full:  public Checksum1 (asm 16-bit lanes + CHAR_OFFSET + packing)
//
// Run on server:
//   ./bench_linux -test.bench='Micro' -test.benchtime=1s -test.count=3

func BenchmarkChecksumMicro(b *testing.B) {
	sizes := []int{1024, 65536}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		tag := fmt.Sprintf("%dKB", size/1024)

		b.Run("Raw/"+tag, func(b *testing.B) {
			b.SetBytes(int64(size))
			for b.Loop() {
				var s1, s2 uint32
				checksum1AVX2(data, &s1, &s2)
			}
		})

		b.Run("Full/"+tag, func(b *testing.B) {
			b.SetBytes(int64(size))
			for b.Loop() {
				Checksum1(data)
			}
		})
	}
}
