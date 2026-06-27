// SSE2/SSSE3 checksum: 32B/iter, VPMADDUBSW + VPMADDWD (XMM).
// VPADDW + VPMADDWD used for s1, VPMADDWD for s2 weighted.
// CHAR_OFFSET post-correction in Go.
//
// ⚠️  Same Go Plan 9 operand swap applies: VPMADDUBSW(signed src1, unsigned src2).

#include "textflag.h"

// func checksum1SSE2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1SSE2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI
	MOVQ    data_len+8(FP), SI
	CMPQ    SI, $32
	JL      bail

	MOVQ    s1+24(FP), CX
	MOVQ    s2+32(FP), R8

	// ── Tables (128-bit) ──
	LEAQ    ones_sse<>+0(SB), AX
	MOVOU   (AX), X15               // byte-ones (for VPMADDUBSW)
	MOVOU   16(AX), X5              // int16-ones (for VPMADDWD)
	LEAQ    mul_T2_sse<>+0(SB), AX
	MOVOU   (AX), X7                // weights [32..17]
	MOVOU   16(AX), X13             // weights [16..1]

	// ── Save initial values (applied as scalars at exit) ──
	MOVL    (CX), R13               // R13 = init_s1
	MOVL    (R8), DX                // DX  = init_s2

	// ── Zero accumulators ──
	PXOR    X12, X12                // Σ weighted byte sums
	PXOR    X4, X4                  // Σ s1_before_k
	PXOR    X14, X14                // running byte-sum (init_s1 added as scalar at exit, not broadcast)

	// Preload first 32B
	MOVOU   0(DI), X2               // first 16B
	MOVOU   16(DI), X8              // second 16B

	ANDQ    $~31, SI
	SHRQ    $5, SI                  // iterations = len/32
	MOVQ    SI, R12                 // R12 = N
	ADDQ    $32, DI

loop:
	// ═══════════════════════════════════════
	// s1: VPADDW merge halves + VPMADDWD pair-sum (2 insns replaces VPHADDW+2×VPUNPCK+VPADDD)
	// ═══════════════════════════════════════
	VPMADDUBSW X15, X2, X0          // first 16B → 8 int16
	VPMADDUBSW X15, X8, X1          // second 16B → 8 int16
	VPADDW X1, X0, X0               // merge halves → 8 int16
	VPMADDWD X5, X0, X0             // pair-sum (int16_ones) → 4 int32 delta_s1

	// s2: accumulate s1_before
	VPADDD  X4, X14, X4

	// s2: weighted — VPMADDWD pair-sum replaces VPUNPCK+VPUNPCK+VPADDD
	VPMADDUBSW X7, X2, X2           // first 16B × [32..17]
	VPMADDWD X5, X2, X2             // pair-sum → 4 int32

	VPMADDUBSW X13, X8, X6          // second 16B × [16..1]
	VPMADDWD X5, X6, X6             // pair-sum → 4 int32

	VPADDD  X6, X2, X2
	VPADDD  X12, X2, X12

	// Prefetch 6 cachelines ahead
	PREFETCHT0 384(DI)

	// s1 update
	VPADDD  X14, X0, X14

	// Next block
	SUBQ    $1, SI
	JZ      done
	MOVOU   0(DI), X2
	MOVOU   16(DI), X8
	ADDQ    $32, DI
	JMP     loop

done:
	// Reduce X14 → s1
	VPSRLDQ $8, X14, X0
	VPADDD  X0, X14, X14
	VPSRLDQ $4, X14, X0
	VPADDD  X0, X14, X14
	MOVD    X14, R10
	ADDL    R13, R10               // s1 = byte_sum + init_s1

	// Reduce X4
	VPSRLDQ $8, X4, X0
	VPADDD  X0, X4, X4
	VPSRLDQ $4, X4, X0
	VPADDD  X0, X4, X4
	MOVD    X4, R9
	SHLL    $5, R9                 // R9 = 32 × Σ s1_before

	// s2 correction: 32 × N × init_s1
	MOVL    R12, R11
	IMULL   R13, R11
	SHLL    $5, R11
	ADDL    R11, R9

	// Reduce X12
	VPSRLDQ $8, X12, X0
	VPADDD  X0, X12, X12
	VPSRLDQ $4, X12, X0
	VPADDD  X0, X12, X12
	MOVD    X12, R11
	ADDL    R9, R11
	ADDL    DX, R11                // s2 += init_s2

	MOVL    R10, (CX)
	MOVL    R11, (R8)

	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// ── All-1s table (32 bytes: 16B byte-ones + 16B int16-ones) ──
DATA ones_sse<>+0(SB)/8, $0x0101010101010101
DATA ones_sse<>+8(SB)/8, $0x0101010101010101
DATA ones_sse<>+16(SB)/8, $0x0001000100010001
DATA ones_sse<>+24(SB)/8, $0x0001000100010001
GLOBL ones_sse<>(SB), RODATA|NOPTR, $32

// ── Weight table for 32B window: [32,31,...,1] as LE uint64 ──
DATA mul_T2_sse<>+0(SB)/8,  $0x191a1b1c1d1e1f20  // 32,31,30,29,28,27,26,25
DATA mul_T2_sse<>+8(SB)/8,  $0x1112131415161718  // 24,23,22,21,20,19,18,17
DATA mul_T2_sse<>+16(SB)/8, $0x090a0b0c0d0e0f10  // 16,15,14,13,12,11,10, 9
DATA mul_T2_sse<>+24(SB)/8, $0x0102030405060708  //  8, 7, 6, 5, 4, 3, 2, 1
GLOBL mul_T2_sse<>(SB), RODATA|NOPTR, $32
