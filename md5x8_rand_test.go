//go:build amd64

package delta

import (
	"crypto/md5"
	"math/rand"
	"testing"
)

// TestMD5x8_Randomized runs many random combinations to catch edge cases.
func TestMD5x8_Randomized(t *testing.T) {
	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	rng := rand.New(rand.NewSource(42))
	sizes := []int{1, 2, 3, 7, 15, 31, 55, 56, 57, 63, 64, 65, 100, 127, 128, 129, 255, 256, 257,
		511, 512, 513, 700, 1023, 1024, 1025, 2047, 2048, 2049, 4095, 4096, 4097}

	for iter := 0; iter < 100; iter++ {
		n := 2 + rng.Intn(7) // 2..8 blocks
		var offsets, lengths [8]int
		off := 0
		for b := 0; b < n; b++ {
			// Mix of random sizes
			ln := sizes[rng.Intn(len(sizes))]
			offsets[b] = off
			lengths[b] = ln
			off += ln
		}
		data := make([]byte, off)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}

		var outAVX2 [8][16]byte
		md5Hash8wayAVX2(data, offsets, lengths, &outAVX2)

		for b := 0; b < n; b++ {
			expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
			if outAVX2[b] != expected {
				t.Errorf("iter=%d lane=%d len=%d mismatch:\n  got:  %x\n  want: %x",
					iter, b, lengths[b], outAVX2[b], expected)
				t.FailNow()
			}
		}

		// Also compare against pure Go reference
		var outGo [8][16]byte
		md5Hash8wayGo(data, offsets, lengths, &outGo)
		for b := 0; b < n; b++ {
			if outAVX2[b] != outGo[b] {
				t.Errorf("iter=%d lane=%d len=%d AVX2 vs Go mismatch:\n  AVX2: %x\n  Go:   %x",
					iter, b, lengths[b], outAVX2[b], outGo[b])
				t.FailNow()
			}
		}
	}
}

// TestMD5x16_Randomized runs many random combinations for AVX512.
func TestMD5x16_Randomized(t *testing.T) {
	if !md5x16available() {
		t.Skip("AVX512 not available")
	}

	rng := rand.New(rand.NewSource(12345))
	sizes := []int{1, 2, 3, 7, 15, 31, 55, 56, 57, 63, 64, 65, 100, 127, 128, 129, 255, 256, 257,
		511, 512, 513, 700, 1023, 1024, 1025, 2047, 2048, 2049, 4095, 4096, 4097}

	for iter := 0; iter < 100; iter++ {
		n := 2 + rng.Intn(15) // 2..16 blocks
		var offsets, lengths [16]int
		off := 0
		for b := 0; b < n; b++ {
			ln := sizes[rng.Intn(len(sizes))]
			offsets[b] = off
			lengths[b] = ln
			off += ln
		}
		data := make([]byte, off)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}

		var outAVX512 [16][16]byte
		md5Hash16wayAVX512(data, offsets, lengths, &outAVX512)

		for b := 0; b < n; b++ {
			expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
			if outAVX512[b] != expected {
				t.Errorf("iter=%d lane=%d len=%d mismatch:\n  got:  %x\n  want: %x",
					iter, b, lengths[b], outAVX512[b], expected)
				t.FailNow()
			}
		}
	}
}

// TestMD5x8_AllSameTail tests the fast 8way finalization path.
func TestMD5x8_AllSameTail(t *testing.T) {
	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	rng := rand.New(rand.NewSource(99))
	// Test various tail lengths that exercise the 8way finalization paths
	tailLengths := []int{0, 1, 30, 55, 56, 57, 60, 63}

	for _, tailLen := range tailLengths {
		totalLen := 64*10 + tailLen // 10 full chunks + tail
		if totalLen == 0 {
			continue
		}

		data := make([]byte, 8*totalLen)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * totalLen
			lengths[b] = totalLen
		}

		var outAVX2 [8][16]byte
		md5Hash8wayAVX2(data, offsets, lengths, &outAVX2)

		for b := 0; b < 8; b++ {
			expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
			if outAVX2[b] != expected {
				t.Errorf("tailLen=%d lane=%d mismatch:\n  got:  %x\n  want: %x",
					tailLen, b, outAVX2[b], expected)
				t.FailNow()
			}
		}
	}
}

// TestMD5x8_ContiguousBuffer tests the exact buffer layout used in GenerateSignatureReader.
func TestMD5x8_ContiguousBuffer(t *testing.T) {
	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	rng := rand.New(rand.NewSource(77))
	blockSizes := []int{64, 128, 256, 512, 700, 1024, 2048, 4096, 8192}

	for _, blockSize := range blockSizes {
		const batchSize = 8
		batchBuf := make([]byte, batchSize*blockSize)
		for i := range batchBuf {
			batchBuf[i] = byte(rng.Intn(256))
		}

		var off8, len8 [8]int
		off := 0
		for b := 0; b < 8; b++ {
			off8[b] = off
			len8[b] = blockSize
			off += blockSize
		}

		var out8 [8][16]byte
		md5Hash8wayAVX2(batchBuf, off8, len8, &out8)

		for b := 0; b < 8; b++ {
			expected := md5.Sum(batchBuf[b*blockSize : (b+1)*blockSize])
			if out8[b] != expected {
				t.Errorf("blockSize=%d lane=%d mismatch:\n  got:  %x\n  want: %x",
					blockSize, b, out8[b], expected)
				t.FailNow()
			}
		}
	}
}

// TestMD5x16_ContiguousBuffer tests the AVX512 buffer layout used in GenerateSignatureReader.
func TestMD5x16_ContiguousBuffer(t *testing.T) {
	if !md5x16available() {
		t.Skip("AVX512 not available")
	}

	rng := rand.New(rand.NewSource(78))
	blockSizes := []int{2048, 4096, 8192, 16384}

	for _, blockSize := range blockSizes {
		const batchSize = 16
		batchBuf := make([]byte, batchSize*blockSize)
		for i := range batchBuf {
			batchBuf[i] = byte(rng.Intn(256))
		}

		var off16, len16 [16]int
		off := 0
		for b := 0; b < 16; b++ {
			off16[b] = off
			len16[b] = blockSize
			off += blockSize
		}

		var out16 [16][16]byte
		md5Hash16wayAVX512(batchBuf, off16, len16, &out16)

		for b := 0; b < 16; b++ {
			expected := md5.Sum(batchBuf[b*blockSize : (b+1)*blockSize])
			if out16[b] != expected {
				t.Errorf("blockSize=%d lane=%d mismatch:\n  got:  %x\n  want: %x",
					blockSize, b, out16[b], expected)
				t.FailNow()
			}
		}
	}
}
