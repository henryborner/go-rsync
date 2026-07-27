// NEON checksum v10: VUADDLV s1 + WORD VUMULL s2, 64B unrolled (v5 architecture + fixes).
// Fixes: (1) VADDP/VADD operand order (Go 1.26: VADDP Rm,Rn,Rd, VADD Rn,Rm,Rd).
//        (2) Go dispatch p=n-n%64 (was 32). (3) Added raw parity test.
// VUMULL uses byte-packed weights (correct for .8B operands).
// 2+2 VUMULL interleave hides multiply latency on in-order cores.
//
// ⚠️  CBNZ mandatory. NEON clobbers NZCV flags.
// ⚠️  Go 1.26 ARM64 SIMD: VADDP(Vm,Rn,Rd) Rd=pair(Rn,Rm), VADD(Rn,Rm,Rd) Rd=Rn+Rm.

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
	VEOR    V12.B16, V12.B16, V12.B16  // weighted accum

	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]

	BIC     $63, R1, R7         // len & ~63 (single insn vs AND $~63)
	LSR     $6, R7, R7          // N = len/64

loop:
	MOVD    R8, R10              // s1_before_block0

	// Fire weighted WORDs early (long latency), overlap with s1 scalar
	WORD    $0x2E32C045         // VUMULL  V5.8H, V2.8B, V18.8B
	WORD    $0x6E32C046         // VUMULL2 V6.8H, V2.B16, V18.B16

	// s1 block 0 (scalar — runs while VUMULL in pipeline)
	VUADDLV V2.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9      // s2 += 32*s1_before_block0

	MOVD    R8, R11              // s1_before_block1

	// Fire weighted WORDs for block 1 (overlap with s1)
	WORD    $0x2E32C298         // VUMULL  V24.8H, V20.8B, V18.8B
	WORD    $0x6E32C299         // VUMULL2 V25.8H, V20.B16, V18.B16

	// s1 block 1
	VUADDLV V20.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V21.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R11<<5, R9, R9

	// Finish weighted block 0 (VUMULL results ready)
	VADDP   V6.H8, V5.H8, V5.H8 // V5=pair(V5,V6)
	WORD    $0x4E6028A5         // VSADDLP V5.4S, V5.H8
	WORD    $0x2E33C067         // VUMULL  V7.8H, V3.8B, V19.8B
	WORD    $0x6E33C068         // VUMULL2 V8.8H, V3.B16, V19.B16
	VADDP   V8.H8, V7.H8, V7.H8 // V7=pair(V7,V8)
	WORD    $0x4E6028E7         // VSADDLP V7.4S, V7.H8
	VADD    V5.S4, V7.S4, V5.S4 // V5=V5+V7

	// Finish weighted block 1
	VADDP   V25.H8, V24.H8, V24.H8 // V24=pair(V24,V25)
	WORD    $0x4E602B18         // VSADDLP V24.4S, V24.H8
	WORD    $0x2E33C2BA         // VUMULL  V26.8H, V21.8B, V19.8B
	WORD    $0x6E33C2BB         // VUMULL2 V27.8H, V21.B16, V19.B16
	VADDP   V27.H8, V26.H8, V26.H8 // V26=pair(V26,V27)
	WORD    $0x4E602B5A         // VSADDLP V26.4S, V26.H8
	VADD    V24.S4, V26.S4, V24.S4 // V24=V24+V26

	VADD    V5.S4, V24.S4, V5.S4 // V5=V5+V24
	VADD    V12.S4, V5.S4, V12.S4 // V12=V12+V5

	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]
	B       loop

done:
	VADDP   V12.S4, V12.S4, V0.S4 // V0=pair(V12,V12)
	VADDP   V0.S4, V0.S4, V0.S4   // V0=pair(V0,V0) -> scalar
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
