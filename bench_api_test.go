package delta

import (
	"crypto/rand"
	"testing"
)

// makeBenchPair builds a 1MB basis and a newFile with ~10% modified bytes
// (same mutation pattern as BenchmarkSearch). blockSize = CalculateBlockSize.
func makeBenchPair() (basis, newFile []byte, blockSize int32) {
	basis = make([]byte, 1<<20)
	rand.Read(basis)
	newFile = make([]byte, len(basis))
	copy(newFile, basis)
	for i := 0; i < len(newFile)/10; i++ {
		newFile[i*10] ^= 0xFF
	}
	return basis, newFile, CalculateBlockSize(int64(len(basis)))
}

// BenchmarkApplyDelta benchmarks the reconstruct path (ApplyDelta) for two
// instruction profiles:
//   - Match90:   ~90% block references + ~10% literals (typical changed file)
//   - AllLiteral: basis and newFile are unrelated → search finds no matches,
//     the whole file is rebuilt from literal instructions (worst case)
func BenchmarkApplyDelta(b *testing.B) {
	basis, newFile, blockSize := makeBenchPair()
	insts, err := Delta(basis, newFile, blockSize, "md5")
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Match90", func(b *testing.B) {
		b.SetBytes(int64(len(newFile)))
		for b.Loop() {
			if _, err := ApplyDelta(basis, insts, blockSize, "md5"); err != nil {
				b.Fatal(err)
			}
		}
	})

	literalNew := make([]byte, len(basis))
	rand.Read(literalNew)
	litInsts, err := Delta(basis, literalNew, blockSize, "md5")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("AllLiteral", func(b *testing.B) {
		b.SetBytes(int64(len(literalNew)))
		for b.Loop() {
			if _, err := ApplyDelta(basis, litInsts, blockSize, "md5"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRoundTrip measures the full Delta + ApplyDelta + byte-for-byte
// verify pipeline (the API's validation use case).
func BenchmarkRoundTrip(b *testing.B) {
	basis, newFile, blockSize := makeBenchPair()
	b.SetBytes(int64(len(newFile)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := RoundTrip(basis, newFile, blockSize, "md5"); err != nil {
			b.Fatal(err)
		}
	}
}
