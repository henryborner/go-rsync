// Tests for 8-way parallel MD5 (both pure Go reference and AVX2 assembly).
package delta

import (
	"crypto/md5"
	"testing"
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMD5x8_SingleBlock(t *testing.T) {
	// 8 identical 64-byte blocks - should produce 8 identical MD5 hashes
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
	md5Hash8wayGo(data, offsets, lengths, &out)

	expected := md5.Sum(block)
	for b := 0; b < 8; b++ {
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got: %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x8_DifferentBlocks(t *testing.T) {
	// 8 different 700-byte blocks �?each should match its own md5.Sum
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
	md5Hash8wayGo(data, offsets, lengths, &out)

	for b := 0; b < 8; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if out[b] != expected {
			t.Fatalf("lane %d mismatch:\n  got: %x\n  want: %x", b, out[b], expected)
		}
	}
}

func TestMD5x8_AVX2_Parity(t *testing.T) {

	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	// 8 different 700-byte blocks �?AVX2 should match md5.Sum
	data := make([]byte, 8*700)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}

	var offsets, lengths [8]int
	for b := 0; b < 8; b++ {
		offsets[b] = b * 700
		lengths[b] = 700
	}

	var outRef [8][16]byte
	md5Hash8wayGo(data, offsets, lengths, &outRef)

	var outAVX2 [8][16]byte
	md5Hash8wayAVX2(data, offsets, lengths, &outAVX2)

	for b := 0; b < 8; b++ {
		expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
		if outAVX2[b] != expected {
			t.Errorf("AVX2 lane %d vs md5.Sum:\n  got:  %x\n  want: %x", b, outAVX2[b], expected)
		}
		if outAVX2[b] != outRef[b] {
			t.Errorf("AVX2 lane %d vs reference:\n  got:  %x\n  want: %x", b, outAVX2[b], outRef[b])
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
	md5Hash8wayGo(data, offsets, lengths, &out)

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
	md5Hash8wayGo(data, offsets, lengths, &out)

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
		md5Hash8wayGo(data, offsets, lengths, &out)
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

// BenchmarkMD5x8Core_Raw measures PURE md5x8core throughput �?no load-transpose,
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

// BenchmarkLoadTranspose_Scalar benchmarks the scalar load+transpose path.
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
