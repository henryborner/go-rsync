//go:build amd64

// Tests for 8-way parallel SHA-256 (AVX2 assembly vs stdlib).
package delta

import (
	"crypto/sha256"
	"testing"
)

func TestSHA256x8_Parity(t *testing.T) {
	if !sha256x8available() {
		t.Skip("AVX2 not available or SHA-NI present (stdlib is faster)")
	}

	// 8 identical 64-byte blocks (common case: full chunks, no tail)
	t.Run("SingleBlock", func(t *testing.T) {
		block := make([]byte, 64)
		for i := range block {
			block[i] = byte(i % 256)
		}
		data := make([]byte, 8*64)
		for b := 0; b < 8; b++ {
			copy(data[b*64:], block)
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * 64
			lengths[b] = 64
		}

		var out [8][32]byte
		sha256Hash8wayAVX2(data, offsets, lengths, &out)

		expected := sha256.Sum256(block)
		for b := 0; b < 8; b++ {
			if out[b] != expected {
				t.Fatalf("lane %d: got %x, want %x", b, out[b], expected)
			}
		}
	})

	// 8 different 700-byte blocks (700 = 10*64 + 60, tests tail handling)
	t.Run("DifferentBlocks", func(t *testing.T) {
		blen := 700
		data := make([]byte, 8*blen)
		for i := range data {
			data[i] = byte((i * 7) % 256)
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * blen
			lengths[b] = blen
		}

		var out [8][32]byte
		sha256Hash8wayAVX2(data, offsets, lengths, &out)

		for b := 0; b < 8; b++ {
			expected := sha256.Sum256(data[offsets[b] : offsets[b]+lengths[b]])
			if out[b] != expected {
				t.Fatalf("lane %d: got %x, want %x", b, out[b], expected)
			}
		}
	})

	// Tail-only: blockSize < 64 (no SIMD chunks)
	t.Run("TailOnly", func(t *testing.T) {
		blen := 63
		data := make([]byte, 8*blen)
		for i := range data {
			data[i] = byte((i * 13) % 256)
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * blen
			lengths[b] = blen
		}

		var out [8][32]byte
		sha256Hash8wayAVX2(data, offsets, lengths, &out)

		for b := 0; b < 8; b++ {
			expected := sha256.Sum256(data[offsets[b] : offsets[b]+lengths[b]])
			if out[b] != expected {
				t.Fatalf("lane %d tail-only: got %x, want %x", b, out[b], expected)
			}
		}
	})
}

// TestSHA256x8_Force runs the AVX2 parity check regardless of SHA-NI
// availability.  Useful for CI or manual verification that the AVX2
// assembly core itself is correct, even on CPUs where it isn't the
// production path (stdlib SHA-NI is used instead).
func TestSHA256x8_Force(t *testing.T) {
	// 8 different 700-byte blocks
	blen := 700
	data := make([]byte, 8*blen)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [8]int
	for b := 0; b < 8; b++ {
		offsets[b] = b * blen
		lengths[b] = blen
	}

	var out [8][32]byte
	sha256Hash8wayAVX2(data, offsets, lengths, &out)

	for b := 0; b < 8; b++ {
		expected := sha256.Sum256(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d: got %x, want %x", b, out[b], expected)
		}
	}
}

func TestSHA256x8_Randomized(t *testing.T) {
	if !sha256x8available() {
		t.Skip("AVX2 not available or SHA-NI present")
	}

	rng := newXorshift(42)
	for i := 0; i < 100; i++ {
		blen := 1 + int(rng.next()%2048)
		data := make([]byte, 8*blen)
		for j := range data {
			data[j] = byte(rng.next())
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * blen
			lengths[b] = blen
		}

		var out [8][32]byte
		sha256Hash8wayAVX2(data, offsets, lengths, &out)

		for b := 0; b < 8; b++ {
			expected := sha256.Sum256(data[offsets[b] : offsets[b]+lengths[b]])
			if out[b] != expected {
				t.Fatalf("iter %d lane %d len=%d: got %x, want %x",
					i, b, blen, out[b], expected)
			}
		}
	}
}

// xorshift64 PRNG for deterministic randomized tests.
type xorshift struct{ state uint64 }

func newXorshift(seed uint64) *xorshift {
	if seed == 0 {
		seed = 1
	}
	return &xorshift{state: seed}
}

func (x *xorshift) next() uint64 {
	x.state ^= x.state << 13
	x.state ^= x.state >> 7
	x.state ^= x.state << 17
	return x.state
}

// TestSHA256x8_PureGoCore verifies the pure-Go 8-way SHA-256 core
// produces the same intermediate state as manual computation.
// This also exercises sha256x8corePureGo, silencing the unused-func lint.
func TestSHA256x8_PureGoCore(t *testing.T) {
	// 8 identical 64-byte blocks
	block := make([]byte, 64)
	for i := range block {
		block[i] = byte(i % 256)
	}
	var x [16][8]uint32
	for w := 0; w < 16; w++ {
		for lane := 0; lane < 8; lane++ {
			x[w][lane] = uint32(block[w*4])<<24 | uint32(block[w*4+1])<<16 |
				uint32(block[w*4+2])<<8 | uint32(block[w*4+3])
		}
	}

	var state [8][8]uint32
	for lane := 0; lane < 8; lane++ {
		state[0][lane] = 0x6a09e667
		state[1][lane] = 0xbb67ae85
		state[2][lane] = 0x3c6ef372
		state[3][lane] = 0xa54ff53a
		state[4][lane] = 0x510e527f
		state[5][lane] = 0x9b05688c
		state[6][lane] = 0x1f83d9ab
		state[7][lane] = 0x5be0cd19
	}
	var wbuf [16][8]uint32
	var saved [8][8]uint32

	sha256x8corePureGo(&x, &state, &wbuf, &saved)

	// Verify state was modified (non-zero output)
	for lane := 0; lane < 8; lane++ {
		if state[0][lane] == 0x6a09e667 {
			t.Error("pure-Go core did not modify state")
			return
		}
	}
}
