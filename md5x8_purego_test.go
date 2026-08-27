// md5x8_purego.go — 8-way parallel MD5 in pure Go.
// Reference implementation for testing and validation.
// md5x8_purego.go — 纯 Go 8路并行 MD5 参考实现，用于测试和验证。

package delta

import "encoding/binary"

// md5x8 holds 8 parallel MD5 hash states.
type md5x8 struct {
	a, b, c, d [8]uint32
}

// ---- element-wise [8]uint32 operations ----

func vecAdd(a, b [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = a[i] + b[i]
	}
	return r
}

func vecXor(a, b [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = a[i] ^ b[i]
	}
	return r
}

func vecAnd(a, b [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = a[i] & b[i]
	}
	return r
}

func vecAndNot(a, b [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = a[i] &^ b[i]
	}
	return r
}

func vecNot(a [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = ^a[i]
	}
	return r
}

func vecOr(a, b [8]uint32) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = a[i] | b[i]
	}
	return r
}

func vecLeftRotate(a [8]uint32, s uint8) [8]uint32 {
	var r [8]uint32
	for i := 0; i < 8; i++ {
		r[i] = uint32RotateLeft(a[i], int(s))
	}
	return r
}

func uint32RotateLeft(x uint32, n int) uint32 {
	return (x << n) | (x >> (32 - n))
}

func vecBroadcast(v uint32) [8]uint32 {
	return [8]uint32{v, v, v, v, v, v, v, v}
}

// ---- message word loading ----

func load16Words(data []byte, offsets [8]int, remain [8]int) [16][8]uint32 {
	var x [16][8]uint32
	for w := 0; w < 16; w++ {
		for b := 0; b < 8; b++ {
			pos := offsets[b] + w*4
			if pos+4 <= len(data) && pos+4 <= offsets[b]+remain[b] {
				x[w][b] = binary.LittleEndian.Uint32(data[pos : pos+4])
			}
		}
	}
	return x
}

// ---- 8-way MD5 block operation ----

func (m *md5x8) block8way(x [16][8]uint32) {
	a, b, c, d := m.a, m.b, m.c, m.d

	for i := 0; i < 64; i++ {
		var f [8]uint32
		var g int
		switch {
		case i < 16:
			f = vecOr(vecAnd(b, c), vecAndNot(d, b))
			g = i
		case i < 32:
			f = vecOr(vecAnd(b, d), vecAndNot(c, d))
			g = (5*i + 1) % 16
		case i < 48:
			f = vecXor(vecXor(b, c), d)
			g = (3*i + 5) % 16
		default:
			f = vecXor(c, vecOr(b, vecNot(d)))
			g = (7 * i) % 16
		}

		temp := vecAdd(vecAdd(vecAdd(a, f), x[g]), vecBroadcast(t256[i]))
		temp = vecLeftRotate(temp, shifts[i])
		newA := vecAdd(b, temp)

		a, b, c, d = d, newA, b, c
	}

	m.a = vecAdd(m.a, a)
	m.b = vecAdd(m.b, b)
	m.c = vecAdd(m.c, c)
	m.d = vecAdd(m.d, d)
}

func md5x8Init() md5x8 {
	return md5x8{
		a: vecBroadcast(0x67452301),
		b: vecBroadcast(0xefcdab89),
		c: vecBroadcast(0x98badcfe),
		d: vecBroadcast(0x10325476),
	}
}

// md5Hash8wayGo hashes 8 blocks in parallel using pure Go.
// Reference implementation for validation against the AVX2 assembly path.
// md5Hash8wayGo 纯 Go 8路并行 MD5，用于验证 AVX2 汇编路径的正确性。
func md5Hash8wayGo(data []byte, offsets [8]int, lengths [8]int, out *[8][16]byte) {
	m := md5x8Init()

	minFullChunks := lengths[0] / 64
	for b := 1; b < 8; b++ {
		if c := lengths[b] / 64; c < minFullChunks {
			minFullChunks = c
		}
	}

	remain := lengths
	for chunk := 0; chunk < minFullChunks; chunk++ {
		var chunkOffsets [8]int
		for b := 0; b < 8; b++ {
			chunkOffsets[b] = offsets[b] + chunk*64
		}
		x := load16Words(data, chunkOffsets, remain)
		m.block8way(x)
		for b := 0; b < 8; b++ {
			remain[b] -= 64
		}
	}

	for b := 0; b < 8; b++ {
		a, bb, c, d := m.a[b], m.b[b], m.c[b], m.d[b]
		totalLen := uint64(lengths[b])
		processed := minFullChunks * 64
		chunkStart := offsets[b] + processed

		for processed+64 <= lengths[b] {
			chunk := data[chunkStart : chunkStart+64]
			sa, sb, sc, sd := a, bb, c, d

			var x [16]uint32
			for j := 0; j < 16; j++ {
				x[j] = binary.LittleEndian.Uint32(chunk[j*4 : (j+1)*4])
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
				a, bb, c, d = d, bb+uint32RotateLeft(f, int(shifts[step])), bb, c
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
