//go:build amd64

package delta

import (
	"encoding/binary"
	"math/bits"

	"golang.org/x/sys/cpu"
)

// md5x8core runs 64 MD5 steps on 8 parallel blocks using AVX2.
// x points to 16 pre-transposed message words (16 × [8]uint32 = 512 bytes).
// state points to [4][8]uint32 (a,b,c,d for 8 lanes, 128 bytes).
//
//go:noescape
func md5x8core(x *[16][8]uint32, state *[4][8]uint32)

// MD5x8CoreForBench is an exported wrapper for benchmarking the pure AVX2 core.
func MD5x8CoreForBench(x *[16][8]uint32, state *[4][8]uint32) {
	md5x8core(x, state)
}

// md5x8TransposeFast is a register-shuffle-based transpose from contiguous buffer.
// Uses VPUNPCK instead of VPINSRD (~80 vs ~320 instructions).
//
//go:noescape
func md5x8TransposeFast(buf *[8][64]byte, x *[16][8]uint32)

// md5x8LoadTransposeGather uses VPGATHERDD to load+transpose 8 scattered blocks.
//
//go:noescape
func md5x8LoadTransposeGather(data []byte, offsets *[8]int, chunk int, x *[16][8]uint32)

// md5x8LoadTransposeScalar uses VPINSRD to load+transpose.
//
//go:noescape
func md5x8LoadTransposeScalar(data []byte, offsets *[8]int, chunk int, x *[16][8]uint32)

// md5x8available reports whether AVX2 is supported.
func md5x8available() bool {
	return cpu.X86.HasAVX2
}

// md5Hash8wayAVX2 hashes 8 blocks using the AVX2-accelerated path.
// Requires AVX2 support (checked by caller or at init).
func md5Hash8wayAVX2(data []byte, offsets [8]int, lengths [8]int, out *[8][16]byte) {

	var state [4][8]uint32
	state[0] = [8]uint32{0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301}
	state[1] = [8]uint32{0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89}
	state[2] = [8]uint32{0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe}
	state[3] = [8]uint32{0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476}

	// Find common full-chunk count (same as pure Go logic)
	minFullChunks := lengths[0] / 64
	for b := 1; b < 8; b++ {
		if c := lengths[b] / 64; c < minFullChunks {
			minFullChunks = c
		}
	}

	var x [16][8]uint32

	// Phase 1: 8-way AVX2 for common full chunks.
	// VPGATHERDD loads 8 scattered blocks in a single pass per word.
	for chunk := 0; chunk < minFullChunks; chunk++ {
		md5x8LoadTransposeGather(data, &offsets, chunk, &x)
		md5x8core(&x, &state)
	}

	// Phase 2: handle remaining chunks + tails.
	// Check if all blocks have identical tail length (common for GenerateSignature).
	allSameTail := true
	tailLen := lengths[0] - minFullChunks*64
	if tailLen >= 64 {
		allSameTail = false // has remaining full chunks, needs scalar
	}
	for b := 1; b < 8 && allSameTail; b++ {
		remaining := lengths[b] - minFullChunks*64
		if remaining >= 64 || remaining != tailLen {
			allSameTail = false
		}
	}

	if allSameTail && tailLen > 0 {
		md5Finalize8way(data, offsets, minFullChunks, tailLen, lengths, &state, &x, out)
	} else {
		// Slow path: per-lane scalar finalization.
		md5FinalizeScalar(data, offsets, minFullChunks, lengths, &state, out)
	}
}

// md5Finalize8way processes identical tails from 8 blocks using AVX2.
func md5Finalize8way(data []byte, offsets [8]int, fullChunks int, tailLen int,
	lengths [8]int, state *[4][8]uint32, x *[16][8]uint32,
	out *[8][16]byte) {

	var buf [8][64]byte

	if tailLen < 56 {
		// One padding chunk fits in 64 bytes.
		for b := 0; b < 8; b++ {
			tailStart := offsets[b] + fullChunks*64
			copy(buf[b][:], data[tailStart:tailStart+tailLen])
			buf[b][tailLen] = 0x80
			for i := tailLen + 1; i < 56; i++ {
				buf[b][i] = 0
			}
			binary.LittleEndian.PutUint64(buf[b][56:], uint64(lengths[b])*8)
		}
		md5x8TransposeFast(&buf, x)
		md5x8core(x, state)
	} else {
		// Two padding chunks needed (tailLen >= 56).
		// Chunk 1: tail + 0x80 + zeros up to 64 bytes.
		for b := 0; b < 8; b++ {
			tailStart := offsets[b] + fullChunks*64
			copy(buf[b][:], data[tailStart:tailStart+tailLen])
			buf[b][tailLen] = 0x80
			for i := tailLen + 1; i < 64; i++ {
				buf[b][i] = 0
			}
		}
		md5x8TransposeFast(&buf, x)
		md5x8core(x, state)

		// Chunk 2: all zeros + 8-byte length.
		// Skip buf + transpose — set x directly.
		for i := 0; i < 16; i++ {
			x[i] = [8]uint32{}
		}
		for b := 0; b < 8; b++ {
			x[14][b] = uint32(lengths[b] * 8)
		}
		md5x8core(x, state)
	}

	// Extract final digests from state.
	for b := 0; b < 8; b++ {
		var dig [16]byte
		binary.LittleEndian.PutUint32(dig[0:], state[0][b])
		binary.LittleEndian.PutUint32(dig[4:], state[1][b])
		binary.LittleEndian.PutUint32(dig[8:], state[2][b])
		binary.LittleEndian.PutUint32(dig[12:], state[3][b])
		out[b] = dig
	}
}

// md5FinalizeScalar processes remaining chunks and tails per-lane (fallback).
func md5FinalizeScalar(data []byte, offsets [8]int, fullChunks int,
	lengths [8]int, state *[4][8]uint32, out *[8][16]byte) {

	for b := 0; b < 8; b++ {
		a, bb, c, d := state[0][b], state[1][b], state[2][b], state[3][b]
		totalLen := uint64(lengths[b])
		processed := fullChunks * 64
		chunkStart := offsets[b] + processed

		for processed+64 <= lengths[b] {
			chunk := data[chunkStart : chunkStart+64]
			sa, sb, sc, sd := a, bb, c, d

			var x2 [16]uint32
			for j := 0; j < 16; j++ {
				x2[j] = uint32(chunk[j*4]) | uint32(chunk[j*4+1])<<8 |
					uint32(chunk[j*4+2])<<16 | uint32(chunk[j*4+3])<<24
			}

			for step := 0; step < 64; step++ {
				var f uint32
				var g int
				switch {
				case step < 16:
					f = (bb & c) | (^bb & d)
					g = step
				case step < 32:
					f = (bb & d) | (c & ^d)
					g = (5*step + 1) % 16
				case step < 48:
					f = bb ^ c ^ d
					g = (3*step + 5) % 16
				default:
					f = c ^ (bb | ^d)
					g = (7 * step) % 16
				}
				f = f + a + x2[g] + t256[step]
				a, bb, c, d = d, bb+bits.RotateLeft32(f, int(shifts[step])), bb, c
			}
			a += sa
			bb += sb
			c += sc
			d += sd
			processed += 64
			chunkStart += 64
		}

		tail := data[chunkStart : offsets[b]+lengths[b]]
		out[b] = md5FinalLane(a, bb, c, d, tail, totalLen)
	}
}
