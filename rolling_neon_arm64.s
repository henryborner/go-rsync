// NEON checksum v9: VUXTL+VMLAL s2 (fixed weights + all 4 halves per block), VUADDLV s1, 64B unrolled.
// Fixes v7: (1) weight table now halfword-packed for VMLAL .4H/.8H (was byte-packed for VUMULL).
//           (2) V5/V6 and V24/V25 now get VMLAL (were discarded).
//           (3) VADDP mnemonic replaced with WORD — Go 1.26 ARM64 asm generates wrong VADDP encoding.
// 16 VMLAL per 64B; V12 serial latency hidden by s1 scalar interleave.
//
// ⚠️  CBNZ mandatory. NEON clobbers NZCV flags.
// ⚠️  VADDP must use WORD, NOT mnemonic — Go 1.26 assembler bug.

#include "textflag.h"

// func checksum1NEON(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0
	MOVD    data_len+8(FP), R1
	CMP     $64, R1
	BLT     bail

	MOVD    s1+24(FP), R2
	MOVD    s2+32(FP), R3

	// Load weight table: 4×.8H halfword weights (verified GNU as)
	// V16 = {32..25}, V17 = {24..17}, V18 = {16..9}, V19 = {8..1}
	MOVD    $w32_neon<>(SB), R4
	VLD1    (R4), [V16.B16]
	ADD     $16, R4, R4
	VLD1    (R4), [V17.B16]
	ADD     $16, R4, R4
	VLD1    (R4), [V18.B16]
	ADD     $16, R4, R4
	VLD1    (R4), [V19.B16]

	MOVWU   (R2), R8            // R8 = s1 (scalar)
	MOVWU   (R3), R9            // R9 = s2 (scalar)
	VEOR    V12.B16, V12.B16, V12.B16  // VMLAL dest accumulator

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

	// s1 block0 (scalar fills VMLAL latency)
	VUADDLV V2.B16, V26; VMOV V26.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V26; VMOV V26.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9

	// Block 0: VMLAL (8 insns — all 4 halves × 4 weights)
	WORD    $0x2E70800C         // VMLAL  V12.4S, V0.4H, V16.4H  (b0..3 × 32..29)
	WORD    $0x6E70800C         // VMLAL2 V12.4S, V0.8H, V16.8H  (b4..7 × 28..25)
	WORD    $0x2E71802C         // VMLAL  V12.4S, V1.4H, V17.4H  (b8..11 × 24..21)
	WORD    $0x6E71802C         // VMLAL2 V12.4S, V1.8H, V17.8H  (b12..15 × 20..17)
	WORD    $0x2E7280AC         // VMLAL  V12.4S, V5.4H, V18.4H  (b16..19 × 16..13)
	WORD    $0x6E7280AC         // VMLAL2 V12.4S, V5.8H, V18.8H  (b20..23 × 12..9)
	WORD    $0x2E7380CC         // VMLAL  V12.4S, V6.4H, V19.4H  (b24..27 × 8..5)
	WORD    $0x6E7380CC         // VMLAL2 V12.4S, V6.8H, V19.8H  (b28..31 × 4..1)

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

	// Block 1: VMLAL (8 insns — reuse V16..V19 weights)
	WORD    $0x2E7082CC         // VMLAL  V12.4S, V22.4H, V16.4H
	WORD    $0x6E7082CC         // VMLAL2 V12.4S, V22.8H, V16.8H
	WORD    $0x2E7182EC         // VMLAL  V12.4S, V23.4H, V17.4H
	WORD    $0x6E7182EC         // VMLAL2 V12.4S, V23.8H, V17.8H
	WORD    $0x2E72830C         // VMLAL  V12.4S, V24.4H, V18.4H
	WORD    $0x6E72830C         // VMLAL2 V12.4S, V24.8H, V18.8H
	WORD    $0x2E73832C         // VMLAL  V12.4S, V25.4H, V19.4H
	WORD    $0x6E73832C         // VMLAL2 V12.4S, V25.8H, V19.8H

	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]
	B       loop

done:
	WORD    $0x4EACBD80         // VADDP V0.4S, V12.4S, V12.4S
	WORD    $0x4EA0BC00         // VADDP V0.4S, V0.4S, V0.4S
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

// Weight table: halfword-packed for VMLAL .4H/.8H (NOT byte-packed like VUMULL).
// V16 = weights 32..25, V17 = 24..17, V18 = 16..9, V19 = 8..1.
// Each entry is a 16-bit halfword (e.g. 32 = 0x0020).
DATA w32_neon<>+0(SB)/8,  $0x001D001E001F0020   // V16 low:  32,31,30,29
DATA w32_neon<>+8(SB)/8,  $0x0019001A001B001C   // V16 high: 28,27,26,25
DATA w32_neon<>+16(SB)/8, $0x0015001600170018   // V17 low:  24,23,22,21
DATA w32_neon<>+24(SB)/8, $0x0011001200130014   // V17 high: 20,19,18,17
DATA w32_neon<>+32(SB)/8, $0x000D000E000F0010   // V18 low:  16,15,14,13
DATA w32_neon<>+40(SB)/8, $0x0009000A000B000C   // V18 high: 12,11,10,9
DATA w32_neon<>+48(SB)/8, $0x0005000600070008   // V19 low:  8,7,6,5
DATA w32_neon<>+56(SB)/8, $0x0001000200030004   // V19 high: 4,3,2,1
GLOBL w32_neon<>(SB), RODATA|NOPTR, $64

