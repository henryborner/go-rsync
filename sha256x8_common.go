// SHA-256 8-way common definitions shared between pure Go reference
// and the AVX2 assembly path.

package delta

import (
	"encoding/binary"
	"math/bits"
)

// SHA-256 round constants K[0..63]
// First 32 bits of the fractional parts of the cube roots of the first 64 primes.
var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
	0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
	0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
	0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
	0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
	0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
	0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
	0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
	0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

// sha256FinalLane computes the final SHA-256 digest for one lane.
// SHA-256 padding: append 1 bit (0x80 byte), then zeros, then 64-bit big-endian length.
// Unlike MD5 which uses little-endian, SHA-256 uses BIG-ENDIAN for the length.
func sha256FinalLane(state [8]uint32, tail []byte, totalLen uint64) [32]byte {
	blockLen := len(tail)
	padLen := 0
	if blockLen < 56 {
		padLen = 56 - blockLen
	} else {
		padLen = 64 + 56 - blockLen
	}

	var bufArr [128]byte
	buf := bufArr[:blockLen+padLen+8]
	copy(buf, tail)
	buf[blockLen] = 0x80

	// SHA-256: big-endian 64-bit length (MD5 uses little-endian!)
	binary.BigEndian.PutUint64(buf[blockLen+padLen:], totalLen*8)

	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3],
		state[4], state[5], state[6], state[7]

	for i := 0; i < len(buf); i += 64 {
		chunk := buf[i : i+64]
		sa, sb, sc, sd, se, sf, sg, sh := a, b, c, d, e, f, g, h

		var w [64]uint32
		for j := 0; j < 16; j++ {
			w[j] = binary.BigEndian.Uint32(chunk[j*4 : (j+1)*4])
		}
		for j := 16; j < 64; j++ {
			s0 := bits.RotateLeft32(w[j-15], -7) ^ bits.RotateLeft32(w[j-15], -18) ^ (w[j-15] >> 3)
			s1 := bits.RotateLeft32(w[j-2], -17) ^ bits.RotateLeft32(w[j-2], -19) ^ (w[j-2] >> 10)
			w[j] = w[j-16] + s0 + w[j-7] + s1
		}

		for j := 0; j < 64; j++ {
			S1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
			ch := (e & f) ^ (^e & g)
			t1 := h + S1 + ch + sha256K[j] + w[j]
			S0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
			maj := (a & b) ^ (a & c) ^ (b & c)
			t2 := S0 + maj

			h = g
			g = f
			f = e
			e = d + t1
			d = c
			c = b
			b = a
			a = t1 + t2
		}

		a += sa
		b += sb
		c += sc
		d += sd
		e += se
		f += sf
		g += sg
		h += sh
	}

	var digest [32]byte
	binary.BigEndian.PutUint32(digest[0:], a)
	binary.BigEndian.PutUint32(digest[4:], b)
	binary.BigEndian.PutUint32(digest[8:], c)
	binary.BigEndian.PutUint32(digest[12:], d)
	binary.BigEndian.PutUint32(digest[16:], e)
	binary.BigEndian.PutUint32(digest[20:], f)
	binary.BigEndian.PutUint32(digest[24:], g)
	binary.BigEndian.PutUint32(digest[28:], h)
	return digest
}
