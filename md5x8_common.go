// Shared MD5 8-way definitions used by both the pure Go reference
// (md5x8_test.go) and the AVX2 assembly path (md5x8_amd64.s/.go).

package delta

import (
	"encoding/binary"
	"math"
	"math/bits"
)

// MD5 round constants: T[i] = floor(2^32 * |sin(i+1)|)
var t256 [64]uint32

// shift amounts per step
var shifts = [64]uint8{
	7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
	5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
	4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
	6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
}

func init() {
	for i := 0; i < 64; i++ {
		t256[i] = uint32(0x100000000 * math.Abs(math.Sin(float64(i+1))))
	}
}

// md5FinalLane computes the final MD5 digest for one lane after all data chunks.
// Uses a fixed-size stack buffer [128]byte to avoid heap allocation.
// Max padding size = 63(tail) + 57(pad) + 8(len) = 128 bytes.
func md5FinalLane(a, b, c, d uint32, tail []byte, totalLen uint64) [16]byte {
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
	binary.LittleEndian.PutUint64(buf[blockLen+padLen:], totalLen*8)

	for i := 0; i < len(buf); i += 64 {
		chunk := buf[i : i+64]
		sa, sb, sc, sd := a, b, c, d

		var x [16]uint32
		for j := 0; j < 16; j++ {
			x[j] = binary.LittleEndian.Uint32(chunk[j*4 : (j+1)*4])
		}

		for step := 0; step < 64; step++ {
			var f uint32
			var g int
			switch {
			case step < 16:
				f = (b & c) | (^b & d)
				g = step
			case step < 32:
				f = (b & d) | (c & ^d)
				g = (5*step + 1) % 16
			case step < 48:
				f = b ^ c ^ d
				g = (3*step + 5) % 16
			default:
				f = c ^ (b | ^d)
				g = (7 * step) % 16
			}
			f = f + a + x[g] + t256[step]
			a, b, c, d = d, b+bits.RotateLeft32(f, int(shifts[step])), b, c
		}

		a += sa
		b += sb
		c += sc
		d += sd
	}

	var out [16]byte
	binary.LittleEndian.PutUint32(out[0:], a)
	binary.LittleEndian.PutUint32(out[4:], b)
	binary.LittleEndian.PutUint32(out[8:], c)
	binary.LittleEndian.PutUint32(out[12:], d)
	return out
}
