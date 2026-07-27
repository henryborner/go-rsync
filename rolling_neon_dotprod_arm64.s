//go:build arm64

// NEON checksum v13: UDOT s2, 128B unrolled, VLD1 contiguous V2-V5.
// 8 UDOT per 128B iteration. SUB/CBNZ once per 128B.
// Requires ARMv8.2+dotprod.
//
// ⚠️  UDOT WORD encodings: GNU as -march=armv8.2-a+dotprod

#include "textflag.h"

// func checksum1NEON_dotprod(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON_dotprod(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0
	MOVD    data_len+8(FP), R1
	CMP     $128, R1
	BLT     bail

	MOVD    s1+24(FP), R2
	MOVD    s2+32(FP), R3

	MOVD    $w32_dotprod<>(SB), R4
	VLD1    (R4), [V18.B16]
	ADD     $16, R4, R4
	VLD1    (R4), [V19.B16]

	MOVWU   (R2), R8
	MOVWU   (R3), R9
	VEOR    V12.B16, V12.B16, V12.B16

	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V4.B16, V5.B16]

	BIC     $127, R1, R7
	LSR     $7, R7, R7          // N = len/128

loop:
	// ====== Part 1: first 64B (preloaded V2-V5) ======
	MOVD    R8, R10

	WORD    $0x6E92944C         // UDOT V12.4S, V2.16B, V18.16B
	WORD    $0x6E93946C         // UDOT V12.4S, V3.16B, V19.16B

	VUADDLV V2.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9

	MOVD    R8, R11

	WORD    $0x6E92948C         // UDOT V12.4S, V4.16B, V18.16B
	WORD    $0x6E9394AC         // UDOT V12.4S, V5.16B, V19.16B

	VUADDLV V4.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V5.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R11<<5, R9, R9

	// Load second 64B
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V4.B16, V5.B16]

	// ====== Part 2: second 64B (just loaded) ======
	MOVD    R8, R10

	WORD    $0x6E92944C         // UDOT V12.4S, V2.16B, V18.16B
	WORD    $0x6E93946C         // UDOT V12.4S, V3.16B, V19.16B

	VUADDLV V2.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9

	MOVD    R8, R11

	WORD    $0x6E92948C         // UDOT V12.4S, V4.16B, V18.16B
	WORD    $0x6E9394AC         // UDOT V12.4S, V5.16B, V19.16B

	VUADDLV V4.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V5.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R11<<5, R9, R9

	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V4.B16, V5.B16]
	B       loop

done:
	VADDP   V12.S4, V12.S4, V0.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R4
	ADD     R4, R9

	MOVW    R8, (R2)
	MOVW    R9, (R3)
	MOVD    $1, R10
	MOVB    R10, ret+40(FP)
	RET

bail:
	MOVD    $0, R10
	MOVB    R10, ret+40(FP)
	RET

DATA w32_dotprod<>+0(SB)/8,  $0x191a1b1c1d1e1f20
DATA w32_dotprod<>+8(SB)/8,  $0x1112131415161718
DATA w32_dotprod<>+16(SB)/8, $0x090a0b0c0d0e0f10
DATA w32_dotprod<>+24(SB)/8, $0x0102030405060708
GLOBL w32_dotprod<>(SB), RODATA|NOPTR, $32
