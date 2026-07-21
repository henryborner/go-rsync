package delta

import (
	"crypto/rand"
	"fmt"
	"testing"
)

func BenchmarkSignatureParallel(b *testing.B) {
	sizes := []int{1024 * 1024, 10 * 1024 * 1024, 100 * 1024 * 1024}
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		bs := CalculateBlockSize(int64(sz))
		b.Run(fmt.Sprintf("%dMB", sz/1024/1024), func(b *testing.B) {
			b.SetBytes(int64(sz))
			for b.Loop() {
				GenerateSignatureParallel(data, bs, "md5")
			}
		})
	}
}
