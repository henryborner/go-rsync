//go:build amd64

package delta

import "golang.org/x/sys/cpu"

// checksum1 computes the initial rolling checksum for data.
// Uses AVX2 assembly on supported CPUs, falls back to 128B Go batch.
func checksum1(data []byte) (s1, s2 uint32) {
	n := len(data)
	if n == 0 {
		return 0, 0
	}

	// AVX2 assembly (64B/iter)
	if cpu.X86.HasAVX2 && n >= 64 && checksum1AVX2(data, &s1, &s2) {
		p := n - n%64
		s1 += uint32(p) * CHAR_OFFSET
		s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
		for i := p; i < n; i++ {
			s1 += uint32(data[i]) + CHAR_OFFSET
			s2 += s1
		}
		return s1, s2
	}

	// SSE2 assembly (32B/iter, SSSE3 actually — all amd64 CPUs)
	if n >= 32 && checksum1SSE2(data, &s1, &s2) {
		p := n - n%32
		s1 += uint32(p) * CHAR_OFFSET
		s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
		for i := p; i < n; i++ {
			s1 += uint32(data[i]) + CHAR_OFFSET
			s2 += s1
		}
		return s1, s2
	}

	// Go 128B batch fallback
	i := 0
	// 128B main loop: 4 unrolled 32B rsync-verified batches
	for i+128 <= n {
		// Batch 0 (bytes 0..31)
		var g0 [8]uint32
		var sw0 uint32
		for g := 0; g < 8; g++ {
			j := g * 4
			b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
			g0[g] = b0 + b1 + b2 + b3
			sw0 += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sw0 + 28*g0[0] + 24*g0[1] + 20*g0[2] + 16*g0[3] + 12*g0[4] + 8*g0[5] + 4*g0[6] + 528*CHAR_OFFSET
		s1 += g0[0] + g0[1] + g0[2] + g0[3] + g0[4] + g0[5] + g0[6] + g0[7] + 32*CHAR_OFFSET

		// Batch 1 (bytes 32..63)
		var g1 [8]uint32
		var sw1 uint32
		for g := 0; g < 8; g++ {
			j := 32 + g*4
			b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
			g1[g] = b0 + b1 + b2 + b3
			sw1 += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sw1 + 28*g1[0] + 24*g1[1] + 20*g1[2] + 16*g1[3] + 12*g1[4] + 8*g1[5] + 4*g1[6] + 528*CHAR_OFFSET
		s1 += g1[0] + g1[1] + g1[2] + g1[3] + g1[4] + g1[5] + g1[6] + g1[7] + 32*CHAR_OFFSET

		// Batch 2 (bytes 64..95)
		var g2 [8]uint32
		var sw2 uint32
		for g := 0; g < 8; g++ {
			j := 64 + g*4
			b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
			g2[g] = b0 + b1 + b2 + b3
			sw2 += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sw2 + 28*g2[0] + 24*g2[1] + 20*g2[2] + 16*g2[3] + 12*g2[4] + 8*g2[5] + 4*g2[6] + 528*CHAR_OFFSET
		s1 += g2[0] + g2[1] + g2[2] + g2[3] + g2[4] + g2[5] + g2[6] + g2[7] + 32*CHAR_OFFSET

		// Batch 3 (bytes 96..127)
		var g3 [8]uint32
		var sw3 uint32
		for g := 0; g < 8; g++ {
			j := 96 + g*4
			b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
			g3[g] = b0 + b1 + b2 + b3
			sw3 += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sw3 + 28*g3[0] + 24*g3[1] + 20*g3[2] + 16*g3[3] + 12*g3[4] + 8*g3[5] + 4*g3[6] + 528*CHAR_OFFSET
		s1 += g3[0] + g3[1] + g3[2] + g3[3] + g3[4] + g3[5] + g3[6] + g3[7] + 32*CHAR_OFFSET

		i += 128
	}

	// 32B tail batches
	for i+32 <= n {
		var gs [8]uint32
		var sw uint32
		for g := 0; g < 8; g++ {
			j := g * 4
			b0, b1, b2, b3 := uint32(data[i+j]), uint32(data[i+j+1]), uint32(data[i+j+2]), uint32(data[i+j+3])
			gs[g] = b0 + b1 + b2 + b3
			sw += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sw + 28*gs[0] + 24*gs[1] + 20*gs[2] + 16*gs[3] + 12*gs[4] + 8*gs[5] + 4*gs[6] + 528*CHAR_OFFSET
		s1 += gs[0] + gs[1] + gs[2] + gs[3] + gs[4] + gs[5] + gs[6] + gs[7] + 32*CHAR_OFFSET
		i += 32
	}
	for ; i < n; i++ {
		s1 += uint32(data[i]) + CHAR_OFFSET
		s2 += s1
	}
	return s1, s2
}
