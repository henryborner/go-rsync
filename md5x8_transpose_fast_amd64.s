// md5x8TransposeFast — register-based 8×16 dword transpose from contiguous
// [8][64]byte buffer to [16][8]uint32 transposed words.
// Uses full-YMM loads + VPUNPCK shuffles instead of VPINSRD scalar inserts.
// ~80 instructions vs ~320 for md5x8transpose.
//
// buf: [8][64]byte — block0_64B, ..., block7_64B (contiguous, 512 bytes)
// x:   [16][8]uint32 — output
//
// Algorithm: two passes. Each pass transposes 8 blocks × 8 dwords (low/high halves).
// 1. VPUNPCKLDQ/VPUNPCKHDQ: interleave pairs of blocks → 4-dword groups
// 2. VPUNPCKLQDQ/VPUNPCKHQDQ: merge across 4-block groups → 2-dword groups
// 3. VINSERTI128/VEXTRACTI128: merge across 128-bit lanes → fully transposed

#include "textflag.h"

// func md5x8TransposeFast(buf *[8][64]byte, x *[16][8]uint32)
TEXT ·md5x8TransposeFast(SB), NOSPLIT, $0-16
	MOVQ buf+0(FP), R8    // R8 = buf
	MOVQ x+8(FP), R9      // R9 = x

	// ═══════════════════════════════════════════════
	// Pass 1: Low 32 bytes of each block → words 0..7
	// ═══════════════════════════════════════════════

	// Load low halves: Y0..Y7 = blocks 0..7 low 32B (dwords 0..7)
	VMOVDQU 0(R8), Y0
	VMOVDQU 64(R8), Y1
	VMOVDQU 128(R8), Y2
	VMOVDQU 192(R8), Y3
	VMOVDQU 256(R8), Y4
	VMOVDQU 320(R8), Y5
	VMOVDQU 384(R8), Y6
	VMOVDQU 448(R8), Y7

	// Step 1: interleave dwords from pairs → T0..T7 in Y8..Y15
	VPUNPCKLDQ Y1, Y0, Y8    // T0 = [B0.D0,B1.D0,B0.D1,B1.D1 | B0.D4,B1.D4,B0.D5,B1.D5]
	VPUNPCKHDQ Y1, Y0, Y9    // T1 = [B0.D2,B1.D2,B0.D3,B1.D3 | B0.D6,B1.D6,B0.D7,B1.D7]
	VPUNPCKLDQ Y3, Y2, Y10   // T2
	VPUNPCKHDQ Y3, Y2, Y11   // T3
	VPUNPCKLDQ Y5, Y4, Y12   // T4
	VPUNPCKHDQ Y5, Y4, Y13   // T5
	VPUNPCKLDQ Y7, Y6, Y14   // T6
	VPUNPCKHDQ Y7, Y6, Y15   // T7

	// Step 2: merge across T pairs (qword interleave) → back into Y0..Y7
	VPUNPCKLQDQ Y10, Y8, Y0  // U0 = [B0.D0,B1.D0,B2.D0,B3.D0 | B0.D4,B1.D4,B2.D4,B3.D4]
	VPUNPCKHQDQ Y10, Y8, Y1  // U1
	VPUNPCKLQDQ Y11, Y9, Y2  // U2
	VPUNPCKHQDQ Y11, Y9, Y3  // U3
	VPUNPCKLQDQ Y14, Y12, Y4 // U4 = [B4.D0,B5.D0,B6.D0,B7.D0 | B4.D4,B5.D4,B6.D4,B7.D4]
	VPUNPCKHQDQ Y14, Y12, Y5 // U5
	VPUNPCKLQDQ Y15, Y13, Y6 // U6
	VPUNPCKHQDQ Y15, Y13, Y7 // U7

	// Step 3: merge 128-bit lanes → transposed words 0..7
	// Save Ui_high before modifying Yi, then:
	//   word_i   = [Ui_low  | U(i+4)_low]  via VINSERTI128 $1 (upper←src)
	//   word_i+4 = [Ui_high | U(i+4)_high] via VINSERTI128 $0 (lower←src)

	VEXTRACTI128 $1, Y0, X8     // X8 = U0_high (save)
	VINSERTI128  $1, X4, Y0, Y0 // Y0 = [U0_low  | U4_low]  = word 0
	VINSERTI128  $0, X8, Y4, Y4 // Y4 = [U0_high | U4_high] = word 4

	VEXTRACTI128 $1, Y1, X10
	VINSERTI128  $1, X5, Y1, Y1 // word 1
	VINSERTI128  $0, X10, Y5, Y5 // word 5

	VEXTRACTI128 $1, Y2, X12
	VINSERTI128  $1, X6, Y2, Y2 // word 2
	VINSERTI128  $0, X12, Y6, Y6 // word 6

	VEXTRACTI128 $1, Y3, X14
	VINSERTI128  $1, X7, Y3, Y3 // word 3
	VINSERTI128  $0, X14, Y7, Y7 // word 7

	// Store low-half results: Y0..Y7 → x[0..7]
	VMOVDQU Y0, 0(R9)
	VMOVDQU Y1, 32(R9)
	VMOVDQU Y2, 64(R9)
	VMOVDQU Y3, 96(R9)
	VMOVDQU Y4, 128(R9)
	VMOVDQU Y5, 160(R9)
	VMOVDQU Y6, 192(R9)
	VMOVDQU Y7, 224(R9)

	// ═══════════════════════════════════════════════
	// Pass 2: High 32 bytes of each block → words 8..15
	// ═══════════════════════════════════════════════

	// Load high halves: Y0..Y7 = blocks 0..7 high 32B (dwords 8..15)
	VMOVDQU 32(R8), Y0
	VMOVDQU 96(R8), Y1
	VMOVDQU 160(R8), Y2
	VMOVDQU 224(R8), Y3
	VMOVDQU 288(R8), Y4
	VMOVDQU 352(R8), Y5
	VMOVDQU 416(R8), Y6
	VMOVDQU 480(R8), Y7

	// Step 1
	VPUNPCKLDQ Y1, Y0, Y8
	VPUNPCKHDQ Y1, Y0, Y9
	VPUNPCKLDQ Y3, Y2, Y10
	VPUNPCKHDQ Y3, Y2, Y11
	VPUNPCKLDQ Y5, Y4, Y12
	VPUNPCKHDQ Y5, Y4, Y13
	VPUNPCKLDQ Y7, Y6, Y14
	VPUNPCKHDQ Y7, Y6, Y15

	// Step 2
	VPUNPCKLQDQ Y10, Y8, Y0
	VPUNPCKHQDQ Y10, Y8, Y1
	VPUNPCKLQDQ Y11, Y9, Y2
	VPUNPCKHQDQ Y11, Y9, Y3
	VPUNPCKLQDQ Y14, Y12, Y4
	VPUNPCKHQDQ Y14, Y12, Y5
	VPUNPCKLQDQ Y15, Y13, Y6
	VPUNPCKHQDQ Y15, Y13, Y7

	// Step 3
	VEXTRACTI128 $1, Y0, X8
	VINSERTI128  $1, X4, Y0, Y0  // word 8
	VINSERTI128  $0, X8, Y4, Y4  // word 12

	VEXTRACTI128 $1, Y1, X10
	VINSERTI128  $1, X5, Y1, Y1  // word 9
	VINSERTI128  $0, X10, Y5, Y5 // word 13

	VEXTRACTI128 $1, Y2, X12
	VINSERTI128  $1, X6, Y2, Y2  // word 10
	VINSERTI128  $0, X12, Y6, Y6 // word 14

	VEXTRACTI128 $1, Y3, X14
	VINSERTI128  $1, X7, Y3, Y3  // word 11
	VINSERTI128  $0, X14, Y7, Y7 // word 15

	// Store high-half results: Y0..Y7 → x[8..15]
	VMOVDQU Y0, 256(R9)
	VMOVDQU Y1, 288(R9)
	VMOVDQU Y2, 320(R9)
	VMOVDQU Y3, 352(R9)
	VMOVDQU Y4, 384(R9)
	VMOVDQU Y5, 416(R9)
	VMOVDQU Y6, 448(R9)
	VMOVDQU Y7, 480(R9)

	RET
