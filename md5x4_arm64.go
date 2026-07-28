//go:build arm64

package delta

import (
	"encoding/binary"
	"math/bits"

	"golang.org/x/sys/cpu"
)

// md5x4available reports whether NEON is supported.
func md5x4available() bool {
	return cpu.ARM64.HasASIMD
}

// MD5x4available is the exported version for external use.
func MD5x4available() bool { return md5x4available() }

// md5x4core runs 64 MD5 steps on 4 parallel blocks in pure Go.
// The Go compiler may auto-vectorize this to NEON on ARM64.
// x[16] holds the transposed message words: x[word][lane].
// state[4][4] holds a,b,c,d for 4 lanes.
func md5x4core(x *[16][4]uint32, state *[4][4]uint32) {
	a, b, c, d := &state[0], &state[1], &state[2], &state[3]
	sa := *a
	sb := *b
	sc := *c
	sd := *d

	for step := 0; step < 64; step++ {
		var g int
		var f [4]uint32
		switch {
		case step < 16:
			g = step
			for i := 0; i < 4; i++ {
				f[i] = (b[i] & c[i]) | (^b[i] & d[i])
			}
		case step < 32:
			g = (5*step + 1) % 16
			for i := 0; i < 4; i++ {
				f[i] = (b[i] & d[i]) | (c[i] & ^d[i])
			}
		case step < 48:
			g = (3*step + 5) % 16
			for i := 0; i < 4; i++ {
				f[i] = b[i] ^ c[i] ^ d[i]
			}
		default:
			g = (7 * step) % 16
			for i := 0; i < 4; i++ {
				f[i] = c[i] ^ (b[i] | ^d[i])
			}
		}
		for i := 0; i < 4; i++ {
			f[i] += a[i] + x[g][i] + t256[step]
			f[i] = bits.RotateLeft32(f[i], int(shifts[step]))
			a[i] = b[i] + f[i]
		}
		// Register rotation: a←d, b←(new)a, c←b, d←c
		*a, *b, *c, *d = *d, *a, *b, *c
	}

	// After 64 steps (64%4==0), rotation returns to original positions.
	// Add back initial state.
	for i := 0; i < 4; i++ {
		a[i] += sa[i]
		b[i] += sb[i]
		c[i] += sc[i]
		d[i] += sd[i]
	}
}

// md5Hash4wayNEON hashes 4 blocks using the NEON-accelerated path.
// Requires NEON support (checked by caller).
func md5Hash4wayNEON(data []byte, offsets [4]int, lengths [4]int, out *[4][16]byte) {

	var state [4][4]uint32
	state[0] = [4]uint32{0x67452301, 0x67452301, 0x67452301, 0x67452301}
	state[1] = [4]uint32{0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89}
	state[2] = [4]uint32{0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe}
	state[3] = [4]uint32{0x10325476, 0x10325476, 0x10325476, 0x10325476}

	// Find common full-chunk count
	minFullChunks := lengths[0] / 64
	for b := 1; b < 4; b++ {
		if c := lengths[b] / 64; c < minFullChunks {
			minFullChunks = c
		}
	}

	var x [16][4]uint32
	var buf [4][64]byte

	// Phase 1: 4-way NEON for common full chunks.
	for chunk := 0; chunk < minFullChunks; chunk++ {
		for b := 0; b < 4; b++ {
			start := offsets[b] + chunk*64
			copy(buf[b][:], data[start:start+64])
		}
		// Transpose 4 blocks of 16 uint32 words into 16 groups of 4 lanes.
		// Block b contributes 16 words to buf[b][0:64].
		// x[word][lane] = binary.LittleEndian.Uint32(buf[lane][word*4:])
		for w := 0; w < 16; w++ {
			for lane := 0; lane < 4; lane++ {
				x[w][lane] = binary.LittleEndian.Uint32(buf[lane][w*4:])
			}
		}
		md5x4core(&x, &state)
	}

	// Phase 2: handle remaining chunks + tails per-lane.
	for b := 0; b < 4; b++ {
		a, bb, c, d := state[0][b], state[1][b], state[2][b], state[3][b]
		totalLen := uint64(lengths[b])
		processed := minFullChunks * 64
		chunkStart := offsets[b] + processed

		for processed+64 <= lengths[b] {
			var x2 [16]uint32
			for j := 0; j < 16; j++ {
				x2[j] = binary.LittleEndian.Uint32(data[chunkStart+j*4:])
			}
			sa, sb, sc, sd := a, bb, c, d
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
