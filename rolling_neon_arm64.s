// NEON checksum v7: VUXTL+VMLAL s2 (direct accumulate), VUADDLV s1, 64B unrolled.
// VMLAL serializes thru V12 but 5-cycle latency gives room for other NEON ops.
//
// ⚠️  CBNZ mandatory. NEON clobbers NZCV flags.

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

	MOVWU   (R2), R8            // R8 = s1 (scalar)
	MOVWU   (R3), R9            // R9 = s2 (scalar)
	VEOR    V12.B16, V12.B16, V12.B16  // weighted accum (VMLAL dest!)

	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]

	BIC     $63, R1, R7
	LSR     $6, R7, R7          // N = len/64

loop:
	MOVD    R8, R10              // s1_before_block0

	// === Block 0: VUXTL widen bytes → halfwords ===
	WORD    $0x2F08A440         // VUXTL  V0.8H, V2.8B
	WORD    $0x6F08A441         // VUXTL2 V1.8H, V2.16B
	WORD    $0x2F08A465         // VUXTL  V5.8H, V3.8B
	WORD    $0x6F08A466         // VUXTL2 V6.8H, V3.16B

	// s1 block0 (scalar, fills VMLAL latency gap)
	VUADDLV V2.B16, V22; VMOV V22.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V22; VMOV V22.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9

	// Block 0: VMLAL accumulate (serial thru V12, ~5cy latency = room for s1)
	WORD    $0x2E72800C         // VMLAL  V12.4S, V0.4H, V18.4H
	WORD    $0x6E72800C         // VMLAL2 V12.4S, V0.8H, V18.8H
	WORD    $0x2E73802C         // VMLAL  V12.4S, V1.4H, V19.4H
	WORD    $0x6E73802C         // VMLAL2 V12.4S, V1.8H, V19.8H

	MOVD    R8, R11              // s1_before_block1

	// === Block 1: VUXTL widen ===
	WORD    $0x2F08A696         // VUXTL  V22.8H, V20.8B
	WORD    $0x6F08A697         // VUXTL2 V23.8H, V20.16B
	WORD    $0x2F08A6B8         // VUXTL  V24.8H, V21.8B
	WORD    $0x6F08A6B9         // VUXTL2 V25.8H, V21.16B

	// s1 block1
	VUADDLV V20.B16, V26; VMOV V26.S[0], R4; ADD R4, R8
	VUADDLV V21.B16, V26; VMOV V26.S[0], R4; ADD R4, R8
	ADD     R11<<5, R9, R9

	// Block 1: VMLAL accumulate
	WORD    $0x2E7282CC         // VMLAL  V12.4S, V22.4H, V18.4H
	WORD    $0x6E7282CC         // VMLAL2 V12.4S, V22.8H, V18.8H
	WORD    $0x2E7382EC         // VMLAL  V12.4S, V23.4H, V19.4H
	WORD    $0x6E7382EC         // VMLAL2 V12.4S, V23.8H, V19.8H

	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]
	B       loop

done:
	VADDP   V0.S4, V12.S4, V12.S4
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

DATA w32_neon<>+0(SB)/8,  $0x191a1b1c1d1e1f20
DATA w32_neon<>+8(SB)/8,  $0x1112131415161718
DATA w32_neon<>+16(SB)/8, $0x090a0b0c0d0e0f10
DATA w32_neon<>+24(SB)/8, $0x0102030405060708
GLOBL w32_neon<>(SB), RODATA|NOPTR, $32
