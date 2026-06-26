//go:build amd64

package delta

import (
	"encoding/binary"

	"golang.org/x/sys/cpu"
)

// md5x16core runs 64 MD5 steps on 16 parallel blocks using AVX512.
// x points to 16 pre-transposed message words (16 × [16]uint32 = 1024 bytes).
// state points to [4][16]uint32 (a,b,c,d for 16 lanes, 256 bytes).
//
//go:noescape
func md5x16core(x *[16][16]uint32, state *[4][16]uint32)

// md5x16LoadTransposeGather loads 64 bytes from 16 scattered blocks and
// transposes directly into 16 ZMM words using VPGATHERDD with k-mask.
//
//go:noescape
func md5x16LoadTransposeGather(data []byte, offsets *[16]int, chunk int, x *[16][16]uint32)

// md5x16available reports whether AVX512 is supported.
func md5x16available() bool {
	return cpu.X86.HasAVX512F && cpu.X86.HasAVX512DQ
}

// MD5x16available is the exported version for external use.
func MD5x16available() bool { return md5x16available() }

// MD5x16CoreForBench is an exported wrapper for benchmarking the pure AVX512 core.
func MD5x16CoreForBench(x *[16][16]uint32, state *[4][16]uint32) {
	md5x16core(x, state)
}

// md5Hash16wayAVX512 hashes 16 blocks using the AVX512-accelerated path.
func md5Hash16wayAVX512(data []byte, offsets [16]int, lengths [16]int, out *[16][16]byte) {

	var state [4][16]uint32
	for i := 0; i < 16; i++ {
		state[0][i] = 0x67452301
		state[1][i] = 0xefcdab89
		state[2][i] = 0x98badcfe
		state[3][i] = 0x10325476
	}

	// Find common full-chunk count
	minFullChunks := lengths[0] / 64
	for b := 1; b < 16; b++ {
		if c := lengths[b] / 64; c < minFullChunks {
			minFullChunks = c
		}
	}

	var x [16][16]uint32

	// Phase 1: 16-way AVX512 for common full chunks.
	// Single VPGATHERDD-based load+transpose per chunk (no Go copy needed).
	for chunk := 0; chunk < minFullChunks; chunk++ {
		md5x16LoadTransposeGather(data, &offsets, chunk, &x)
		md5x16core(&x, &state)
	}

	// Phase 2: handle remaining chunks + tails per lane (scalar fallback).
	for b := 0; b < 16; b++ {
		a, bb, c, d := state[0][b], state[1][b], state[2][b], state[3][b]
		totalLen := uint64(lengths[b])
		processed := minFullChunks * 64
		chunkStart := offsets[b] + processed

		// Process any remaining full 64-byte chunks
		for processed+64 <= lengths[b] {
			chunk := data[chunkStart : chunkStart+64]
			sa, sb, sc, sd := a, bb, c, d

			var x16 [16]uint32
			for j := 0; j < 16; j++ {
				x16[j] = binary.LittleEndian.Uint32(chunk[j*4 : (j+1)*4])
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
				temp := a + f + x16[g] + t256[step]
				a, bb, c, d = d, bb+bitsRotateLeft32(temp, int(shifts[step])), bb, c
			}
			a += sa
			bb += sb
			c += sc
			d += sd
			processed += 64
			chunkStart += 64
		}

		// Tail + finalization
		tail := data[chunkStart : offsets[b]+lengths[b]]
		out[b] = md5FinalLane(a, bb, c, d, tail, totalLen)
	}
}

func bitsRotateLeft32(x uint32, n int) uint32 {
	return (x << n) | (x >> (32 - n))
}
