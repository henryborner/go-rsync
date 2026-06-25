// SSE2/SSSE3 checksum: 32B/iter, VPMADDUBSW unsigned + VPUNPCK (XMM).
// Same algorithm as AVX2 but 128-bit registers. Deferred s1 reduction.
// CHAR_OFFSET post-correction in Go.

#include "textflag.h"

// func checksum1SSE2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1SSE2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI
	MOVQ    data_len+8(FP), SI
	CMPQ    SI, $32
	JL      bail

	MOVQ    s1+24(FP), CX
	MOVQ    s2+32(FP), R8

	// Tables (128-bit)
	LEAQ    ones_sse<>+0(SB), AX
	MOVOU   (AX), X15               // all-1s
	LEAQ    mul_T2_sse<>+0(SB), AX
	MOVOU   (AX), X7                // weights [32..17]
	MOVOU   16(AX), X13             // weights [16..1]

	// Initial values
	MOVL    (CX), R10
	MOVD    R10, X0
	PSHUFD  $0, X0, X14            // broadcast init_s1 to 4 lanes
	MOVL    (R8), DX

	// Save for exit
	MOVL    R10, R13

	// Zero
	PXOR    X5, X5
	PXOR    X12, X12
	PXOR    X4, X4

	// Preload first 32B
	MOVOU   0(DI), X2
	MOVOU   16(DI), X8

	ANDQ    $~31, SI
	SHRQ    $5, SI                 // iterations = len/32
	MOVQ    SI, R12
	ADDQ    $32, DI

loop:
	// s1: first 16B
	VPMADDUBSW X15, X2, X0
	VPUNPCKLWD X5, X0, X3
	VPUNPCKHWD X5, X0, X0
	VPADDD  X0, X3, X0

	// s1: second 16B
	VPMADDUBSW X15, X8, X6
	VPUNPCKLWD X5, X6, X3
	VPUNPCKHWD X5, X6, X6
	VPADDD  X6, X3, X6

	VPADDD  X6, X0, X0            // X0 = 4xint32 delta

	// s2: accumulate s1_before
	VPADDD  X4, X14, X4

	// s2: weighted
	VPMADDUBSW X7, X2, X2         // first 16B x [32..17]
	VPUNPCKLWD X5, X2, X3
	VPUNPCKHWD X5, X2, X2
	VPADDD  X2, X3, X2

	VPMADDUBSW X13, X8, X6        // second 16B x [16..1]
	VPUNPCKLWD X5, X6, X3
	VPUNPCKHWD X5, X6, X6
	VPADDD  X6, X3, X6

	VPADDD  X6, X2, X2
	VPADDD  X12, X2, X12

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
	// Reduce X14 -> s1
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
	SHLL    $5, R9                 // R9 = 32 x Sum s1_before

	// s2 init correction: 32 x N x init_s1
	MOVL    R12, R11
	IMULL   R13, R11
	SHLL    $5, R11                // R11 = 32 x N x init_s1
	ADDL    R11, R9

	// Reduce X12
	VPSRLDQ $8, X12, X0
	VPADDD  X0, X12, X12
	VPSRLDQ $4, X12, X0
	VPADDD  X0, X12, X12
	MOVD    X12, R11
	ADDL    R9, R11
	ADDL    DX, R11

	MOVL    R10, (CX)
	MOVL    R11, (R8)

	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// All-ones table (16 bytes)
DATA ones_sse<>+0(SB)/8, $0x0101010101010101
DATA ones_sse<>+8(SB)/8, $0x0101010101010101
GLOBL ones_sse<>(SB), RODATA|NOPTR, $16

// Weight table for 32B window: [32,31,...,1] as LE uint64
DATA mul_T2_sse<>+0(SB)/8,  $0x191a1b1c1d1e1f20  // 32,31,30,29,28,27,26,25
DATA mul_T2_sse<>+8(SB)/8,  $0x1112131415161718  // 24,23,22,21,20,19,18,17
DATA mul_T2_sse<>+16(SB)/8, $0x090a0b0c0d0e0f10  // 16,15,14,13,12,11,10, 9
DATA mul_T2_sse<>+24(SB)/8, $0x0102030405060708  //  8, 7, 6, 5, 4, 3, 2, 1
GLOBL mul_T2_sse<>(SB), RODATA|NOPTR, $32
