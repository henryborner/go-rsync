// 8-way parallel MD5 reference implementation in pure Go.
// Uses [8]uint32 arrays to simulate AVX2 YMM registers.
// This validates the algorithm before translating to Plan 9 assembly.
package delta

import (
	"crypto/md5"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// 8-way SIMD MD5 state
// ---------------------------------------------------------------------------

type md5x8 struct {
	a, b, c, d [8]uint32 // 8 parallel hash states
}

// ---------------------------------------------------------------------------
// SIMD helper: element-wise operations on [8]uint32
// In assembly these become single VPADDD / VPXOR / VPAND instructions.
// ---------------------------------------------------------------------------

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

// vecBroadcast creates a [8]uint32 all set to the same value.
func vecBroadcast(v uint32) [8]uint32 {
	return [8]uint32{v, v, v, v, v, v, v, v}
}

// ---------------------------------------------------------------------------
// Load 16 message words (X[0..15]) from 8 different blocks.
// Each X[j] is a [8]uint32: the j-th 32-bit word from each of the 8 blocks.
//
// In assembly: 16 × VPGATHERDD instructions, each gathering 8 dwords from
// 8 scattered memory locations (stride = blockSize).
// ---------------------------------------------------------------------------

func load16Words(data []byte, offsets [8]int, remain [8]int) [16][8]uint32 {
	var x [16][8]uint32
	for w := 0; w < 16; w++ {
		for b := 0; b < 8; b++ {
			pos := offsets[b] + w*4
			if pos+4 <= len(data) && pos+4 <= offsets[b]+remain[b] {
				x[w][b] = binary.LittleEndian.Uint32(data[pos : pos+4])
			}
			// else: zero (we only call this for full 64-byte chunks)
		}
	}
	return x
}

// ---------------------------------------------------------------------------
// MD5 8-way block operation: process one 64-byte chunk from each of 8 blocks.
// This is the core SIMD loop — 64 steps, each using element-wise vector ops.
// ---------------------------------------------------------------------------

func (m *md5x8) block8way(x [16][8]uint32) {
	a, b, c, d := m.a, m.b, m.c, m.d

	for i := 0; i < 64; i++ {
		var f [8]uint32
		var g int
		switch {
		case i < 16: // Round 1: F(b,c,d) = (b & c) | (~b & d), g = i
			f = vecOr(vecAnd(b, c), vecAndNot(d, b))
			g = i
		case i < 32: // Round 2: G(b,c,d) = (b & d) | (c & ~d), g = (5*i+1)%16
			f = vecOr(vecAnd(b, d), vecAndNot(c, d))
			g = (5*i + 1) % 16
		case i < 48: // Round 3: H(b,c,d) = b ^ c ^ d, g = (3*i+5)%16
			f = vecXor(vecXor(b, c), d)
			g = (3*i + 5) % 16
		default: // Round 4: I(b,c,d) = c ^ (b | ~d), g = (7*i)%16
			f = vecXor(c, vecOr(b, vecNot(d)))
			g = (7 * i) % 16
		}

		// a = b + leftRotate(a + f + x[g] + T[i], s)
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

// ---------------------------------------------------------------------------
// Initialize 8 MD5 states (standard MD5 IV for all 8 lanes).
// ---------------------------------------------------------------------------

func md5x8Init() md5x8 {
	return md5x8{
		a: vecBroadcast(0x67452301),
		b: vecBroadcast(0xefcdab89),
		c: vecBroadcast(0x98badcfe),
		d: vecBroadcast(0x10325476),
	}
}

// ---------------------------------------------------------------------------
// Hash 8 equal-length blocks in parallel.
// data: source buffer
// offsets: [8]int — starting byte offset of each block
// lengths: [8]int — length of each block
// out: [8][16]byte — output digests
// ---------------------------------------------------------------------------

func md5Hash8way(data []byte, offsets [8]int, lengths [8]int, out *[8][16]byte) {
	m := md5x8Init()

	// Phase 1: 8-way process common full 64-byte chunks (min across all lanes).
	// Lanes with more chunks are processed individually in Phase 2.
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

	// Phase 2: Process remaining full chunks + tail per lane (scalar).
	// This handles lanes with more chunks than minFullChunks.
	for b := 0; b < 8; b++ {
		a, bb, c, d := m.a[b], m.b[b], m.c[b], m.d[b]
		totalLen := uint64(lengths[b])
		processed := minFullChunks * 64
		chunkStart := offsets[b] + processed

		// Process any remaining full 64-byte chunks for this lane
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

		// Tail + finalization
		tail := data[chunkStart : offsets[b]+lengths[b]]
		out[b] = md5FinalLane(a, bb, c, d, tail, totalLen)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMD5x8_SingleBlock(t *testing.T) {
	// 8 identical 64-byte blocks → should produce 8 identical MD5 hashes
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

	var out [8][16]byte
	md5Hash8way(data, offsets, lengths, &out)

	expected := md5.Sum(block)
	for b := 0; b < 8; b++ {
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got: %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x8_DifferentBlocks(t *testing.T) {
	// 8 different 700-byte blocks → each should match its own md5.Sum
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [8]int
	for b := 0; b < 8; b++ {
		offsets[b] = b * 700
		lengths[b] = 700
	}

	var out [8][16]byte
	md5Hash8way(data, offsets, lengths, &out)

	for b := 0; b < 8; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got: %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x8_UnevenLengths(t *testing.T) {
	// Mix of lengths: 63, 64, 65, 127, 128, 129, 700, 1024
	lengthsList := []int{63, 64, 65, 127, 128, 129, 700, 1024}

	var data []byte
	var offsets, lengths [8]int
	off := 0
	for b, ln := range lengthsList {
		offsets[b] = off
		lengths[b] = ln
		off += ln
	}
	data = make([]byte, off)
	for i := range data {
		data[i] = byte(i * 13 % 256)
	}

	var out [8][16]byte
	md5Hash8way(data, offsets, lengths, &out)

	for b, ln := range lengthsList {
		expected := md5.Sum(data[offsets[b] : offsets[b]+ln])
		if out[b] != expected {
			t.Fatalf("lane %d (len=%d) mismatch:\n  got: %x\n  want: %x", b, ln, out[b], expected)
		}
	}
}

func TestMD5x8_LastBlockShorter(t *testing.T) {
	// Simulate the last 8 blocks of a file where the last one is shorter
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte(i * 3 % 256)
	}

	var offsets, lengths [8]int
	for b := 0; b < 7; b++ {
		offsets[b] = b * 700
		lengths[b] = 700
	}
	offsets[7] = 7 * 700
	lengths[7] = 123 // shorter last block

	var out [8][16]byte
	md5Hash8way(data, offsets, lengths, &out)

	for b := 0; b < 8; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d (len=%d) mismatch:\n  got: %x\n  want: %x", b, lengths[b], out[b], expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmark: compare 1×8 scalar (8 sequential md5.Sum calls) vs 8-way SIMD
// ---------------------------------------------------------------------------

func BenchmarkMD5x8_Scalar(b *testing.B) {
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte(i % 256)
	}
	var offsets [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * 700
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 8; j++ {
			md5.Sum(data[offsets[j] : offsets[j]+700])
		}
	}
}

func BenchmarkMD5x8_SIMD(b *testing.B) {
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte(i % 256)
	}
	var offsets, lengths [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * 700
		lengths[i] = 700
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out [8][16]byte
		md5Hash8way(data, offsets, lengths, &out)
	}
}

func BenchmarkMD5x8_ASM(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte(i % 256)
	}
	var offsets, lengths [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * 700
		lengths[i] = 700
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out [8][16]byte
		md5Hash8wayAVX2(data, offsets, lengths, &out)
	}
}

// BenchmarkMD5x8_Bulk measures raw 8-way AVX2 MD5 core throughput.
// 8 blocks × 4096 bytes each = 32KB per call. No tail, no padding, no checksum1.
func BenchmarkMD5x8_Bulk(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}
	const bytesPerBlock = 4096
	data := make([]byte, 8*bytesPerBlock)
	for i := range data {
		data[i] = byte(i % 256)
	}
	var offsets, lengths [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * bytesPerBlock
		lengths[i] = bytesPerBlock
	}

	var out [8][16]byte

	b.SetBytes(8 * bytesPerBlock)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		md5Hash8wayAVX2(data, offsets, lengths, &out)
	}
}

// BenchmarkMD5x8Core_Raw measures PURE md5x8core throughput — no load-transpose,
// no checksum, just ZMM→ZMM transform. Pre-builds transposed x matrix once.
func BenchmarkMD5x8Core_Raw(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}

	// Prepare one transposed chunk (16 words × 8 lanes)
	var x [16][8]uint32
	for w := 0; w < 16; w++ {
		for ln := 0; ln < 8; ln++ {
			x[w][ln] = uint32(w*8 + ln)
		}
	}

	var state [4][8]uint32
	state[0] = [8]uint32{0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301}
	state[1] = [8]uint32{0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89}
	state[2] = [8]uint32{0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe}
	state[3] = [8]uint32{0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476}

	b.SetBytes(64) // one 64-byte block × 8 lanes = 512B per call
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		md5x8core(&x, &state)
	}
}

// BenchmarkMD5x16Core_Raw measures PURE md5x16core throughput (AVX512).
func BenchmarkMD5x16Core_Raw(b *testing.B) {
	if !md5x16available() {
		b.Skip("AVX512 not available")
	}

	var x [16][16]uint32
	for w := 0; w < 16; w++ {
		for ln := 0; ln < 16; ln++ {
			x[w][ln] = uint32(w*16 + ln)
		}
	}

	var state [4][16]uint32
	for ln := 0; ln < 16; ln++ {
		state[0][ln] = 0x67452301
		state[1][ln] = 0xefcdab89
		state[2][ln] = 0x98badcfe
		state[3][ln] = 0x10325476
	}

	b.SetBytes(int64(64 * 16)) // 1024B per call (16 lanes × 64B)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		md5x16core(&x, &state)
	}
}

// BenchmarkMD5x8Core_Bulk calls md5x8core 1000 times in a tight Go loop
// (amortizes Go-call overhead). Equivalent to md5-simd's BenchmarkBlock8-4.
func BenchmarkMD5x8Core_Bulk(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}

	var x [16][8]uint32
	for w := 0; w < 16; w++ {
		for ln := 0; ln < 8; ln++ {
			x[w][ln] = uint32(w*8 + ln)
		}
	}

	var state [4][8]uint32
	state[0] = [8]uint32{0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301, 0x67452301}
	state[1] = [8]uint32{0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89, 0xefcdab89}
	state[2] = [8]uint32{0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe, 0x98badcfe}
	state[3] = [8]uint32{0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476, 0x10325476}

	const N = 1000
	bytesPerOp := int64(N * 64 * 8) // N chunks × 64B × 8 lanes
	b.SetBytes(bytesPerOp)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < N; j++ {
			md5x8core(&x, &state)
		}
	}
}

// BenchmarkMD5x16Core_Bulk same but for AVX512.
func BenchmarkMD5x16Core_Bulk(b *testing.B) {
	if !md5x16available() {
		b.Skip("AVX512 not available")
	}

	var x [16][16]uint32
	for w := 0; w < 16; w++ {
		for ln := 0; ln < 16; ln++ {
			x[w][ln] = uint32(w*16 + ln)
		}
	}

	var state [4][16]uint32
	for ln := 0; ln < 16; ln++ {
		state[0][ln] = 0x67452301
		state[1][ln] = 0xefcdab89
		state[2][ln] = 0x98badcfe
		state[3][ln] = 0x10325476
	}

	const N = 1000
	bytesPerOp := int64(N * 64 * 16) // N chunks × 64B × 16 lanes
	b.SetBytes(bytesPerOp)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < N; j++ {
			md5x16core(&x, &state)
		}
	}
}

// BenchmarkLoadTranspose_Gather vs _Scalar compares the two loading strategies.
func BenchmarkLoadTranspose_Gather(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}
	data := make([]byte, 8*700)
	var offsets [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * 700
	}
	var x [16][8]uint32

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for chunk := 0; chunk < 1000; chunk++ {
			md5x8LoadTransposeGather(data, &offsets, chunk, &x)
		}
	}
}

func BenchmarkLoadTranspose_Scalar(b *testing.B) {
	if !md5x8available() {
		b.Skip("AVX2 not available")
	}
	data := make([]byte, 8*700)
	var offsets [8]int
	for i := 0; i < 8; i++ {
		offsets[i] = i * 700
	}
	var x [16][8]uint32

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for chunk := 0; chunk < 1000; chunk++ {
			md5x8LoadTransposeScalar(data, &offsets, chunk, &x)
		}
	}
}
