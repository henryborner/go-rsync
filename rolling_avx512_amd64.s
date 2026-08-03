// AVX-512 rolling checksum (opt-in): 64B/iter, single ZMM, ~10 insns/loop
// vs 15/16 for AVX2 (no-prefetch / prefetch bodies). 16-bit-lane VPADDW wraps
// mod 2^16 (same math as the AVX2 v7).
// Exposed as Checksum1AVX512 (explicit opt-in) — NOT part of the default
// dispatch. Measured faster than AVX2 only on Intel server Xeons (full-width
// 512-bit integer units) for blocks ≥ 16 KB (+8~27%); slower on AMD Zen 4.
// Caller must check cpu.X86.HasAVX512 before calling (SIGILL otherwise).
//
// ⚠️ Same Go Plan 9 operand swap applies to VPMADDUBSW:
//   Intel:  VPMADDUBSW(unsigned src1, signed src2)
//   Go:     VPMADDUBSW(signed src1, unsigned src2)
//   Usage:  VPMADDUBSW Z_ones(+1 signed), Z_data(unsigned), Z_dst

#include "textflag.h"

// func checksum1AVX512(data []byte, s1, s2 *uint32) bool
// 16-bit-lane raw sums truncated to 16 bits (no CHAR_OFFSET).
TEXT ·checksum1AVX512(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI        // buf ptr
	MOVQ    data_len+8(FP), SI    // len
	CMPQ    SI, $64               // need at least 64 bytes
	JL      bail

	MOVQ    s1+24(FP), CX         // *ps1
	MOVQ    s2+32(FP), R8         // *ps2

	// ── Tables ──
	LEAQ    ones512<>+0(SB), AX
	VMOVDQU64 (AX), Z15           // 64B byte-ones (0x01) for VPMADDUBSW
	LEAQ    weights512<>+0(SB), AX
	VMOVDQU64 (AX), Z7            // weights [64..1]

	// ── Save initial values ──
	MOVL    (CX), R13             // init_s1
	MOVL    (R8), DX              // init_s2

	// ── Zero 16-bit accumulators (32 lanes) ──
	VPXORD  Z12, Z12, Z12         // Σ weighted
	VPXORD  Z4, Z4, Z4            // Σ s1_before
	VPXORD  Z14, Z14, Z14         // running s1

	// Preload first 64B
	VMOVDQU64 0(DI), Z2
	MOVQ    SI, R15               // original len (for remainder)
	ANDQ    $~63, SI
	SHRQ    $6, SI                // iterations = len/64
	MOVQ    SI, R12               // N (for exit correction)
	ADDQ    $64, DI

loop:
	VPMADDUBSW Z15, Z2, Z0        // s1: 32 int16 pair-sums
	VPMADDUBSW Z7, Z2, Z1         // s2 weighted: 32 int16 pair-sums
	VPADDW  Z4, Z14, Z4           // Σ s1_before (before running s1 update)
	VPADDW  Z0, Z14, Z14          // running s1 += delta (wraps mod 2^16)
	VPADDW  Z1, Z12, Z12          // Σ weighted (wraps mod 2^16)
	SUBQ    $1, SI
	JZ      done
	VMOVDQU64 0(DI), Z2
	ADDQ    $64, DI
	JMP     loop

done:
	// ── reduce Z14: 32 int16 → 1 → R10 (s1) ──
	VEXTRACTI64X4 $1, Z14, Y0       // high 16 lanes
	VEXTRACTI64X4 $0, Z14, Y1       // low 16 lanes
	VPADDW  Y0, Y1, Y1              // 16 lanes
	VEXTRACTI128 $1, Y1, X0         // high 8 lanes
	VPADDW  X0, X1, X1              // 8 lanes
	VPSRLDQ $8, X1, X0
	VPADDW  X0, X1, X1              // 4 lanes
	VPSRLDQ $4, X1, X0
	VPADDW  X0, X1, X1              // 2 lanes
	VPSRLDQ $2, X1, X0
	VPADDW  X0, X1, X1              // 1 lane
	VMOVD   X1, R10
	ADDL    R13, R10                // s1 += init_s1

	// ── reduce Z4 → R9 (Σ s1_before) ──
	VEXTRACTI64X4 $1, Z4, Y0
	VEXTRACTI64X4 $0, Z4, Y1
	VPADDW  Y0, Y1, Y1
	VEXTRACTI128 $1, Y1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $8, X1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $4, X1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $2, X1, X0
	VPADDW  X0, X1, X1
	VMOVD   X1, R9

	// ── reduce Z12 → R11 (Σ weighted) ──
	VEXTRACTI64X4 $1, Z12, Y0
	VEXTRACTI64X4 $0, Z12, Y1
	VPADDW  Y0, Y1, Y1
	VEXTRACTI128 $1, Y1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $8, X1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $4, X1, X0
	VPADDW  X0, X1, X1
	VPSRLDQ $2, X1, X0
	VPADDW  X0, X1, X1
	VMOVD   X1, R11

	// s2 = 64·Σs1_before + Σweighted
	MOVL    R9, AX
	SHLL    $6, AX
	ADDL    R11, AX
	MOVL    AX, R11

	// ── Scalar remainder (0..63 bytes) ──
	MOVQ    R15, AX
	ANDQ    $63, AX
	JZ      skip_rem
	MOVQ    AX, SI
rem_loop:
	MOVBQZX (DI), R14
	ADDL    R14, R10
	ADDL    R10, R11
	ADDQ    $1, DI
	DECQ    SI
	JNZ     rem_loop
skip_rem:

	// s2 corrections: 64·N·init_s1 + init_s2
	MOVL    R12, R9
	IMULL   R13, R9
	SHLL    $6, R9
	ADDL    R9, R11
	ADDL    DX, R11

	// Truncate to 16 bits and store.
	ANDL    $0xFFFF, R10
	ANDL    $0xFFFF, R11
	MOVL    R10, (CX)
	MOVL    R11, (R8)

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	VZEROUPPER
	MOVB    $0, ret+40(FP)
	RET

// ── 64B byte-ones (0x01 × 64) ──
DATA ones512<>+0(SB)/8, $0x0101010101010101
DATA ones512<>+8(SB)/8, $0x0101010101010101
DATA ones512<>+16(SB)/8, $0x0101010101010101
DATA ones512<>+24(SB)/8, $0x0101010101010101
DATA ones512<>+32(SB)/8, $0x0101010101010101
DATA ones512<>+40(SB)/8, $0x0101010101010101
DATA ones512<>+48(SB)/8, $0x0101010101010101
DATA ones512<>+56(SB)/8, $0x0101010101010101
GLOBL ones512<>(SB), RODATA|NOPTR, $64

// ── 64B weights [64..1] ──
DATA weights512<>+0(SB)/8, $0x393A3B3C3D3E3F40
DATA weights512<>+8(SB)/8, $0x3132333435363738
DATA weights512<>+16(SB)/8, $0x292A2B2C2D2E2F30
DATA weights512<>+24(SB)/8, $0x2122232425262728
DATA weights512<>+32(SB)/8, $0x191A1B1C1D1E1F20
DATA weights512<>+40(SB)/8, $0x1112131415161718
DATA weights512<>+48(SB)/8, $0x090A0B0C0D0E0F10
DATA weights512<>+56(SB)/8, $0x0102030405060708
GLOBL weights512<>(SB), RODATA|NOPTR, $64
