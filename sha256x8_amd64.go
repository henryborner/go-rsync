//go:build amd64

package delta

import (
	"encoding/binary"

	"golang.org/x/sys/cpu"
)

// sha256x8core runs 64 SHA-256 rounds on 8 parallel blocks using AVX2.
// x points to 16 pre-transposed message words (16 × [8]uint32 = 512 bytes).
// Each word is in x86 little-endian byte order; the core performs BSWAP internally.
// state points to [8][8]uint32 (a,b,c,d,e,f,g,h for 8 lanes, 256 bytes).
// wbuf points to [16][8]uint32 scratch buffer for message schedule (512 bytes).
// saved points to [8][8]uint32 scratch buffer for initial state (256 bytes).
//
//go:noescape
func sha256x8coreASM(x *[16][8]uint32, state *[8][8]uint32, wbuf *[16][8]uint32, saved *[8][8]uint32)

// sha256x8core is the 8-way SHA-256 core.
func sha256x8core(x *[16][8]uint32, state *[8][8]uint32, wbuf *[16][8]uint32, saved *[8][8]uint32) {
	sha256x8coreASM(x, state, wbuf, saved)
}

// sha256x8corePureGo is a pure Go 8-way SHA-256 core used for testing.
func sha256x8corePureGo(x *[16][8]uint32, state *[8][8]uint32, wbuf *[16][8]uint32, saved *[8][8]uint32) {
	_ = wbuf // wbuf is for parity with sha256x8core, unused in pure Go
	*saved = *state

	var w [64][8]uint32
	for i := 0; i < 16; i++ {
		for lane := 0; lane < 8; lane++ {
			w[i][lane] = x[i][lane]
		}
	}
	for i := 16; i < 64; i++ {
		for lane := 0; lane < 8; lane++ {
			s0 := rotr32(w[i-15][lane], 7) ^ rotr32(w[i-15][lane], 18) ^ (w[i-15][lane] >> 3)
			s1 := rotr32(w[i-2][lane], 17) ^ rotr32(w[i-2][lane], 19) ^ (w[i-2][lane] >> 10)
			w[i][lane] = w[i-16][lane] + s0 + w[i-7][lane] + s1
		}
	}

	for i := 0; i < 64; i++ {
		for lane := 0; lane < 8; lane++ {
			a, b, c, d, e, f, g, h :=
				state[0][lane], state[1][lane], state[2][lane], state[3][lane],
				state[4][lane], state[5][lane], state[6][lane], state[7][lane]

			S1 := rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25)
			ch := (e & f) ^ (^e & g)
			t1 := h + S1 + ch + sha256K[i] + w[i][lane]
			S0 := rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22)
			maj := (a & b) ^ (a & c) ^ (b & c)
			t2 := S0 + maj

			state[0][lane] = t1 + t2
			state[1][lane] = a
			state[2][lane] = b
			state[3][lane] = c
			state[4][lane] = d + t1
			state[5][lane] = e
			state[6][lane] = f
			state[7][lane] = g
		}
	}

	for i := 0; i < 8; i++ {
		for lane := 0; lane < 8; lane++ {
			state[i][lane] += saved[i][lane]
		}
	}
}

func bitswap32(x uint32) uint32 {
	return (x>>24)&0xff | (x>>8)&0xff00 | (x<<8)&0xff0000 | (x<<24)&0xff000000
}

// sha256x8available reports whether our AVX2 SHA-256 path should be used.
// We skip it on CPUs with SHA-NI since stdlib's hardware path is faster.
func sha256x8available() bool {
	return cpu.X86.HasAVX2 && !cpuSHA()
}

// cpuSHA reports whether the CPU supports Intel SHA extensions (SHA-NI).
// CPUID leaf 7, sub-leaf 0, EBX bit 29.
func cpuSHA() bool

// SHA256x8Available is the exported version for external use.
func SHA256x8Available() bool { return sha256x8available() }

// sha256Hash8wayAVX2 hashes 8 blocks using the AVX2-accelerated SHA-256 path.
// All 8 blocks must have the same length (blockSize), which is typical for
// GenerateSignature. Offsets point into the shared data buffer.
func sha256Hash8wayAVX2(data []byte, offsets [8]int, lengths [8]int, out *[8][32]byte) {

	// Initialize state: 8 lanes, each with SHA-256 initial values
	var state [8][8]uint32
	for lane := 0; lane < 8; lane++ {
		state[0][lane] = 0x6a09e667 // a
		state[1][lane] = 0xbb67ae85 // b
		state[2][lane] = 0x3c6ef372 // c
		state[3][lane] = 0xa54ff53a // d
		state[4][lane] = 0x510e527f // e
		state[5][lane] = 0x9b05688c // f
		state[6][lane] = 0x1f83d9ab // g
		state[7][lane] = 0x5be0cd19 // h
	}

	// Find common full-chunk count (all blocks same length in GenerateSignature)
	minFullChunks := lengths[0] / 64
	for b := 1; b < 8; b++ {
		if c := lengths[b] / 64; c < minFullChunks {
			minFullChunks = c
		}
	}

	var x [16][8]uint32
	var wbuf [16][8]uint32 // message schedule scratch
	var saved [8][8]uint32 // initial state copy

	// Phase 1: 8-way AVX2 for common full 64-byte chunks.
	// Reuses MD5's VPGATHERDD gather — same data layout.
	// BSWAP: x86 LE → SHA-256 BE, done in Go before calling asm core.
	for chunk := 0; chunk < minFullChunks; chunk++ {
		md5x8LoadTransposeGather(data, &offsets, chunk, &x)
		// BSWAP each word in-place (LE → BE)
		for w := 0; w < 16; w++ {
			for lane := 0; lane < 8; lane++ {
				x[w][lane] = bitswap32(x[w][lane])
			}
		}
		sha256x8core(&x, &state, &wbuf, &saved)
	}

	// Phase 2: per-lane scalar finalization (remaining chunks + tail).
	for b := 0; b < 8; b++ {
		a, bb, c, d, e, f, g, h :=
			state[0][b], state[1][b], state[2][b], state[3][b],
			state[4][b], state[5][b], state[6][b], state[7][b]

		totalLen := uint64(lengths[b])
		processed := minFullChunks * 64
		chunkStart := offsets[b] + processed

		// Process any remaining full 64-byte chunks (scalar)
		for processed+64 <= lengths[b] {
			chunk := data[chunkStart : chunkStart+64]
			sa, sb, sc, sd, se, sf, sg, sh := a, bb, c, d, e, f, g, h

			var w [64]uint32
			for j := 0; j < 16; j++ {
				w[j] = binary.BigEndian.Uint32(chunk[j*4 : (j+1)*4])
			}
			for j := 16; j < 64; j++ {
				s0 := rotr32(w[j-15], 7) ^ rotr32(w[j-15], 18) ^ (w[j-15] >> 3)
				s1 := rotr32(w[j-2], 17) ^ rotr32(w[j-2], 19) ^ (w[j-2] >> 10)
				w[j] = w[j-16] + s0 + w[j-7] + s1
			}

			for j := 0; j < 64; j++ {
				S1 := rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25)
				ch := (e & f) ^ (^e & g)
				t1 := h + S1 + ch + sha256K[j] + w[j]
				S0 := rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22)
				maj := (a & bb) ^ (a & c) ^ (bb & c)
				t2 := S0 + maj

				h = g
				g = f
				f = e
				e = d + t1
				d = c
				c = bb
				bb = a
				a = t1 + t2
			}

			a += sa
			bb += sb
			c += sc
			d += sd
			e += se
			f += sf
			g += sg
			h += sh

			processed += 64
			chunkStart += 64
		}

		// Tail + SHA-256 finalization
		tail := data[chunkStart : offsets[b]+lengths[b]]
		var laneState [8]uint32
		laneState[0], laneState[1], laneState[2], laneState[3] = a, bb, c, d
		laneState[4], laneState[5], laneState[6], laneState[7] = e, f, g, h
		out[b] = sha256FinalLane(laneState, tail, totalLen)
	}
}

// rotr32 right-rotates a uint32 by n bits.
func rotr32(x uint32, n int) uint32 {
	return (x >> n) | (x << (32 - n))
}
