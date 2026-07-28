//go:build arm64

package delta

import (
	"crypto/md5"
	"testing"
)

func TestMD5x4_SingleBlock(t *testing.T) {
	// 4 identical 64-byte blocks → should produce 4 identical MD5 hashes
	block := make([]byte, 64)
	for i := range block {
		block[i] = byte(i % 256)
	}
	data := make([]byte, 4*64)
	for b := 0; b < 4; b++ {
		copy(data[b*64:], block)
	}

	var offsets, lengths [4]int
	for b := 0; b < 4; b++ {
		offsets[b] = b * 64
		lengths[b] = 64
	}

	var out [4][16]byte
	md5Hash4wayNEON(data, offsets, lengths, &out)

	expected := md5.Sum(block)
	for b := 0; b < 4; b++ {
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got:  %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x4_DifferentBlocks(t *testing.T) {
	// 4 different 700-byte blocks → each should match md5.Sum
	data := make([]byte, 4*700)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [4]int
	for b := 0; b < 4; b++ {
		offsets[b] = b * 700
		lengths[b] = 700
	}

	var out [4][16]byte
	md5Hash4wayNEON(data, offsets, lengths, &out)

	for b := 0; b < 4; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got:  %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x4_NEON_Parity(t *testing.T) {
	if !md5x4available() {
		t.Skip("NEON not available")
	}

	// Include MD5 padding boundary: tail<56→1 chunk, tail≥56→2 chunks.
	sizes := []int{55, 56, 57, 63, 64, 128, 255, 700}
	data := make([]byte, 4*700)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [4]int
	off := 0
	for b := 0; b < 4; b++ {
		offsets[b] = off
		lengths[b] = sizes[b]
		off += 700 // enough space for largest block
	}

	var outNEON [4][16]byte
	md5Hash4wayNEON(data, offsets, lengths, &outNEON)

	var outRef [4][16]byte
	md5Hash4wayGo(data, offsets, lengths, &outRef)

	for b := 0; b < 4; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if outNEON[b] != expected {
			t.Errorf("NEON lane %d vs md5.Sum:\n  got:  %x\n  want: %x", b, outNEON[b], expected)
		}
		if outNEON[b] != outRef[b] {
			t.Errorf("NEON lane %d vs pure Go:\n  NEON: %x\n  Go:   %x", b, outNEON[b], outRef[b])
		}
	}
}
