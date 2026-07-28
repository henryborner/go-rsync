package delta

import "math/bits"

// md5Hash4wayGo hashes 4 blocks in parallel using pure Go.
// Reference implementation for validating the NEON assembly path.
// Available on all platforms for logic testing.
func md5Hash4wayGo(data []byte, offsets [4]int, lengths [4]int, out *[4][16]byte) {
	var state [4][4]uint32
	state[0] = [4]uint32{0x67452301, 0x67452301, 0x67452301, 0x67452301}
	state[1] = [4]uint32{0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89}
	state[2] = [4]uint32{0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe}
	state[3] = [4]uint32{0x10325476, 0x10325476, 0x10325476, 0x10325476}

	minFull := lengths[0] / 64
	for b := 1; b < 4; b++ {
		if c := lengths[b] / 64; c < minFull {
			minFull = c
		}
	}

	// Phase 1: 4-way parallel for common full chunks
	for chunk := 0; chunk < minFull; chunk++ {
		var x [16][4]uint32
		for w := 0; w < 16; w++ {
			for lane := 0; lane < 4; lane++ {
				off := offsets[lane] + chunk*64 + w*4
				x[w][lane] = uint32(data[off]) | uint32(data[off+1])<<8 |
					uint32(data[off+2])<<16 | uint32(data[off+3])<<24
			}
		}
		md5x4coreGo(&x, &state)
	}

	// Phase 2: per-lane tail
	for b := 0; b < 4; b++ {
		a, bb, c, d := state[0][b], state[1][b], state[2][b], state[3][b]
		totalLen := uint64(lengths[b])
		processed := minFull * 64
		pos := offsets[b] + processed

		for processed+64 <= lengths[b] {
			sa, sb, sc, sd := a, bb, c, d
			var x [16]uint32
			for j := 0; j < 16; j++ {
				x[j] = uint32(data[pos+j*4]) | uint32(data[pos+j*4+1])<<8 |
					uint32(data[pos+j*4+2])<<16 | uint32(data[pos+j*4+3])<<24
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
				f = f + a + x[g] + t256[step]
				a, bb, c, d = d, bb+bits.RotateLeft32(f, int(shifts[step])), bb, c
			}
			a += sa
			bb += sb
			c += sc
			d += sd
			processed += 64
			pos += 64
		}
		tail := data[pos : offsets[b]+lengths[b]]
		out[b] = md5FinalLane(a, bb, c, d, tail, totalLen)
	}
}

// md5x4coreGo is the pure-Go 4-way MD5 core (matches NEON logic).
func md5x4coreGo(x *[16][4]uint32, state *[4][4]uint32) {
	a := state[0]
	b := state[1]
	c := state[2]
	d := state[3]
	sa, sb, sc, sd := a, b, c, d

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
		// Register rotation: (a,b,c,d) ← (d, a_new, b, c)
		a, b, c, d = d, a, b, c
	}

	for i := 0; i < 4; i++ {
		a[i] += sa[i]
		b[i] += sb[i]
		c[i] += sc[i]
		d[i] += sd[i]
	}
	state[0] = a
	state[1] = b
	state[2] = c
	state[3] = d
}
