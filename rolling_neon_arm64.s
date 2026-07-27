// NEON checksum: 64B/iter (2x32B unrolled), raw sums (CHAR_OFFSET in Go).
// WORD encodings verified by GNU aarch64-as (2026-07-27).
//
// ⚠️  MANDATORY: Use CBNZ/CBZ, never BEQ/BNE. NEON clobbers NZCV flags.
//
// 64B unrolling: process 2 blocks per loop iteration.
// Block 0 uses V0-V8 temps, Block 1 uses V22-V27 temps.
// Accumulators: V4 (s1_before), V12 (weighted), V14 (running s1).

#include "textflag.h"

// func checksum1NEON(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0
	MOVD    data_len+8(FP), R1
	CMP     $64, R1
	BLT     bail

	MOVD    s1+24(FP), R2
	MOVD    s2+32(FP), R3

	MOVD    $w32_neon<>(SB), R4
	VLD1    (R4), [V18.B16]
	ADD     $16, R4, R4
	VLD1    (R4), [V19.B16]

	MOVWU   (R2), R5
	MOVWU   (R3), R6

	VEOR    V12.B16, V12.B16, V12.B16
	VEOR    V4.B16, V4.B16, V4.B16
	VEOR    V14.B16, V14.B16, V14.B16

	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]

	AND     $~63, R1, R7
	LSR     $6, R7, R7          // N = len/64
	MOVD    R7, R12
	// R0 already advanced by VLD1.P in preload

loop:
	// === Block 0: s1 ===
	WORD    $0x6E202840         // VUADDLP V0.8H, V2.B16
	WORD    $0x6E202861         // VUADDLP V1.8H, V3.B16
	VADD    V0.H8, V0.H8, V1.H8
	WORD    $0x4E602800         // VSADDLP V0.4S, V0.8H

	// s1_before for block 0
	VADD    V4.S4, V4.S4, V14.S4

	// === Block 1: s1 ===
	WORD    $0x6E202A96         // VUADDLP V22.8H, V20.B16
	WORD    $0x6E202AB7         // VUADDLP V23.8H, V21.B16
	VADD    V22.H8, V22.H8, V23.H8
	WORD    $0x4E602AD6         // VSADDLP V22.4S, V22.8H

	// Update running s1 (interleaved between blocks)
	VADD    V14.S4, V14.S4, V0.S4

	// === Block 0: weighted first half ===
	WORD    $0x2E32C045         // VUMULL  V5.8H, V2.8B, V18.8B
	WORD    $0x6E32C046         // VUMULL2 V6.8H, V2.B16, V18.B16
	VADDP   V5.H8, V5.H8, V6.H8
	WORD    $0x4E6028A5         // VSADDLP V5.4S, V5.H8

	// === Block 0: weighted second half ===
	WORD    $0x2E33C067         // VUMULL  V7.8H, V3.8B, V19.8B
	WORD    $0x6E33C068         // VUMULL2 V8.8H, V3.B16, V19.B16
	VADDP   V7.H8, V7.H8, V8.H8
	WORD    $0x4E6028E7         // VSADDLP V7.4S, V7.H8

	// s1_before for block 1 (after block 0 delta applied)
	VADD    V14.S4, V14.S4, V22.S4
	VADD    V4.S4, V4.S4, V14.S4

	// === Block 1: weighted first half ===
	WORD    $0x2E32C298         // VUMULL  V24.8H, V20.8B, V18.8B
	WORD    $0x6E32C299         // VUMULL2 V25.8H, V20.B16, V18.B16
	VADDP   V24.H8, V24.H8, V25.H8
	WORD    $0x4E602B18         // VSADDLP V24.4S, V24.H8

	// === Block 1: weighted second half ===
	WORD    $0x2E33C2BA         // VUMULL  V26.8H, V21.8B, V19.8B
	WORD    $0x6E33C2BB         // VUMULL2 V27.8H, V21.B16, V19.B16
	VADDP   V26.H8, V26.H8, V27.H8
	WORD    $0x4E602B5A         // VSADDLP V26.4S, V26.H8

	// Merge block 0 weighted
	VADD    V5.S4, V5.S4, V7.S4

	// Merge block 1 weighted
	VADD    V24.S4, V24.S4, V26.S4

	// Accumulate all weighted
	VADD    V5.S4, V5.S4, V24.S4
	VADD    V12.S4, V12.S4, V5.S4

	// Next iteration — CBNZ is MANDATORY (NEON clobbers flags!)
	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]
	B       loop

done:
	VADDP   V0.S4, V14.S4, V14.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R8
	ADD     R5, R8, R8

	VADDP   V0.S4, V4.S4, V4.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R9
	LSL     $6, R9, R9          // 64 * sum(s1_before)

	MULW    R12, R5, R10
	LSL     $6, R10, R10        // 64 * N * init_s1
	ADD     R10, R9, R9

	VADDP   V0.S4, V12.S4, V12.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R10
	ADD     R9, R10, R10
	ADD     R6, R10, R10

	MOVW    R8, (R2)
	MOVW    R10, (R3)
	MOVD    $1, R10
	MOVB    R10, ret+40(FP)
	RET

bail:
	MOVD    $0, R10
	MOVB    R10, ret+40(FP)
	RET

DATA w32_neon<>+0(SB)/8,  $0x191a1b1c1d1e1f20
DATA w32_neon<>+8(SB)/8,  $0x1112131415161718
DATA w32_neon<>+16(SB)/8, $0x090a0b0c0d0e0f10
DATA w32_neon<>+24(SB)/8, $0x0102030405060708
GLOBL w32_neon<>(SB), RODATA|NOPTR, $32
