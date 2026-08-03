// SSE2/SSSE3 checksum (16-bit-lane): 32B/iter, 16 insns/loop, 4 VPMADDUBSW,
// 0 VPMADDWD. VPADDW wraps mod 2^16 = the truncation itself.
// CHAR_OFFSET post-correction in Go.
//
// Per-block bounds: s1 lane ≤ 510; weighted lane ≤ 16,065 (< 32767, no
// VPMADDUBSW sat); merged weighted lane ≤ 23,970 (< 65536).
//
// ⚠️  Same Go Plan 9 operand swap applies: VPMADDUBSW(signed src1, unsigned src2).

#include "textflag.h"

// func checksum1SSE2(data []byte, s1, s2 *uint32) bool
// 16-bit-lane: raw byte sums truncated to 16 bits. Processes full 32B blocks
// only (tail handled in Go). Reads init_s1/init_s2, applied mod 2^16 at exit.
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
	LEAQ    mul_T2_sse<>+0(SB), AX
	MOVOU   (AX), X7                // weights [32..17]
	MOVOU   16(AX), X13             // weights [16..1]

	// ── Save initial values (applied as scalars at exit) ──
	MOVL    (CX), R13               // R13 = init_s1
	MOVL    (R8), DX                // DX  = init_s2

	// ── Zero 16-bit accumulators ──
	PXOR    X12, X12                // Σ weighted (8 lanes, wraps mod 2^16)
	PXOR    X4, X4                  // Σ s1_before (8 lanes, wraps)
	PXOR    X14, X14                // running s1 (8 lanes, wraps mod 2^16)

	// Preload first 32B
	MOVOU   0(DI), X2               // first 16B
	MOVOU   16(DI), X8              // second 16B

	ANDQ    $~31, SI
	SHRQ    $5, SI                  // iterations = len/32
	MOVQ    SI, R12                 // R12 = N
	ADDQ    $32, DI

	// Conditional prefetch (same rationale as AVX2): PREFETCHT0 helps only
	// for blocks that leave the cache (> ~64 KB). Two loop bodies.
	CMPQ    SI, $2048               // N = len/32 ≥ 2048 → len ≥ 64 KB
	JGE     pf_loop_sse

nopf_loop_sse:
	// s1 (16-bit)
	VPMADDUBSW X15, X2, X0          // first 16B → 8 int16 pair-sums
	VPMADDUBSW X15, X8, X1          // second 16B → 8 int16 pair-sums
	VPADDW X1, X0, X0               // merge halves → 8 int16 delta_s1
	// X4 must capture s1_before BEFORE the running sum is updated.
	VPADDW X4, X14, X4              // X4 += s1_before (wraps)
	VPADDW X0, X14, X14             // running s1 += delta (wraps mod 2^16)

	// s2 weighted (16-bit)
	VPMADDUBSW X7, X2, X2           // first 16B × [32..17] → 8 int16
	VPMADDUBSW X13, X8, X6          // second 16B × [16..1] → 8 int16
	VPADDW X6, X2, X2               // merge halves → 8 int16 per-block weighted
	VPADDW X2, X12, X12             // X12 += weighted (wraps mod 2^16)

	// Next block
	SUBQ    $1, SI
	JZ      done
	MOVOU   0(DI), X2
	MOVOU   16(DI), X8
	ADDQ    $32, DI
	JMP     nopf_loop_sse

pf_loop_sse:
	// s1 (16-bit)
	VPMADDUBSW X15, X2, X0          // first 16B → 8 int16 pair-sums
	VPMADDUBSW X15, X8, X1          // second 16B → 8 int16 pair-sums
	VPADDW X1, X0, X0               // merge halves → 8 int16 delta_s1
	// X4 must capture s1_before BEFORE the running sum is updated.
	VPADDW X4, X14, X4              // X4 += s1_before (wraps)
	VPADDW X0, X14, X14             // running s1 += delta (wraps mod 2^16)

	// s2 weighted (16-bit)
	VPMADDUBSW X7, X2, X2           // first 16B × [32..17] → 8 int16
	VPMADDUBSW X13, X8, X6          // second 16B × [16..1] → 8 int16
	VPADDW X6, X2, X2               // merge halves → 8 int16 per-block weighted
	VPADDW X2, X12, X12             // X12 += weighted (wraps mod 2^16)

	// Prefetch 6 cachelines ahead
	PREFETCHT0 384(DI)

	// Next block
	SUBQ    $1, SI
	JZ      done
	MOVOU   0(DI), X2
	MOVOU   16(DI), X8
	ADDQ    $32, DI
	JMP     pf_loop_sse

done:
	// Reduce X14 → s1 (8 int16 → 1)
	VPSRLDQ $8, X14, X0
	VPADDW  X0, X14, X14
	VPSRLDQ $4, X14, X0
	VPADDW  X0, X14, X14
	VPSRLDQ $2, X14, X0
	VPADDW  X0, X14, X14
	MOVD    X14, R10
	ADDL    R13, R10               // s1 += init_s1

	// Reduce X4 → R9 (Σ s1_before)
	VPSRLDQ $8, X4, X0
	VPADDW  X0, X4, X4
	VPSRLDQ $4, X4, X0
	VPADDW  X0, X4, X4
	VPSRLDQ $2, X4, X0
	VPADDW  X0, X4, X4
	MOVD    X4, R9
	SHLL    $5, R9                 // R9 = 32 × Σ s1_before

	// s2 correction: 32 × N × init_s1
	MOVL    R12, R11
	IMULL   R13, R11
	SHLL    $5, R11
	ADDL    R11, R9

	// Reduce X12 → R11 (Σ weighted)
	VPSRLDQ $8, X12, X0
	VPADDW  X0, X12, X12
	VPSRLDQ $4, X12, X0
	VPADDW  X0, X12, X12
	VPSRLDQ $2, X12, X0
	VPADDW  X0, X12, X12
	MOVD    X12, R11
	ADDL    R9, R11                // s2 = 32·Σs1_before + Σweighted + corr
	ADDL    DX, R11                // s2 += init_s2

	// Truncate to 16 bits and store.
	ANDL    $0xFFFF, R10
	ANDL    $0xFFFF, R11
	MOVL    R10, (CX)
	MOVL    R11, (R8)

	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// ── All-1s table (16B byte-ones; int16-ones half kept for layout stability) ──
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
