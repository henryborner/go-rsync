package delta

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// BenchmarkWireSignature benchmarks wire-format signature encode/decode for a
// 1MB file (~1500 blocks × 700B).
func BenchmarkWireSignature(b *testing.B) {
	basis := make([]byte, 1<<20)
	rand.Read(basis)
	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	var buf bytes.Buffer
	if err := WireEncodeSignature(&buf, sig); err != nil {
		b.Fatal(err)
	}
	encoded := buf.Bytes()

	b.Run("Encode", func(b *testing.B) {
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			var w bytes.Buffer
			if err := WireEncodeSignature(&w, sig); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Decode", func(b *testing.B) {
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			r := bytes.NewReader(encoded)
			if _, err := WireDecodeSignature(r); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkWireInstructions benchmarks wire-format instruction encode/decode
// for a typical 10%-modified 1MB delta (mostly block references, plus the
// literal data embedded in the stream).
func BenchmarkWireInstructions(b *testing.B) {
	basis, newFile, blockSize := makeBenchPair()
	insts, err := Delta(basis, newFile, blockSize, "md5")
	if err != nil {
		b.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WireEncodeInstructions(&buf, insts); err != nil {
		b.Fatal(err)
	}
	encoded := buf.Bytes()

	b.Run("Encode", func(b *testing.B) {
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			var w bytes.Buffer
			if err := WireEncodeInstructions(&w, insts); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Decode", func(b *testing.B) {
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			r := bytes.NewReader(encoded)
			if _, err := WireDecodeInstructions(r); err != nil {
				b.Fatal(err)
			}
		}
	})
}
