// NEON checksum v4: VUADDLV scalar s1 + WORD VUMULL s2, 64B unrolled (2x32B).
// VUADDLV replaces 4 WORD + 3 mnemonic for s1; 64B amortizes loop overhead.
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
	VEOR    V12.B16, V12.B16, V12.B16  // weighted accum

	VLD1.P  32(R0), [V2.B16, V3.B16]
	VLD1.P  32(R0), [V20.B16, V21.B16]

	BIC     $63, R1, R7         // len & ~63 (single insn vs AND $~63)
	LSR     $6, R7, R7          // N = len/64

loop:
	MOVD    R8, R10              // s1_before_block0

	// Fire ALL block0 VUMULL together (max latency hiding, like AVX2)
	WORD    $0x2E32C045         // VUMULL  V5.8H, V2.8B, V18.8B
	WORD    $0x6E32C046         // VUMULL2 V6.8H, V2.B16, V18.B16
	WORD    $0x2E33C067         // VUMULL  V7.8H, V3.8B, V19.8B
	WORD    $0x6E33C068         // VUMULL2 V8.8H, V3.B16, V19.B16

	// s1 block0 (scalar — overlaps with 4 VUMULL in pipeline)
	VUADDLV V2.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V3.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R10<<5, R9, R9

	MOVD    R8, R11              // s1_before_block1

	// Fire ALL block1 VUMULL together
	WORD    $0x2E32C298         // VUMULL  V24.8H, V20.8B, V18.8B
	WORD    $0x6E32C299         // VUMULL2 V25.8H, V20.B16, V18.B16
	WORD    $0x2E33C2BA         // VUMULL  V26.8H, V21.8B, V19.8B
	WORD    $0x6E33C2BB         // VUMULL2 V27.8H, V21.B16, V19.B16

	// s1 block1
	VUADDLV V20.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	VUADDLV V21.B16, V0; VMOV V0.S[0], R4; ADD R4, R8
	ADD     R11<<5, R9, R9

	// Block0: VADDP + VSADDLP (VUMULL results ready)
	VADDP   V5.H8, V5.H8, V6.H8
	WORD    $0x4E6028A5         // VSADDLP V5.4S, V5.H8
	VADDP   V7.H8, V7.H8, V8.H8
	WORD    $0x4E6028E7         // VSADDLP V7.4S, V7.H8
	VADD    V5.S4, V5.S4, V7.S4

	// Block1: VADDP + VSADDLP
	VADDP   V24.H8, V24.H8, V25.H8
	WORD    $0x4E602B18         // VSADDLP V24.4S, V24.H8
	VADDP   V26.H8, V26.H8, V27.H8
	WORD    $0x4E602B5A         // VSADDLP V26.4S, V26.H8
	VADD    V24.S4, V24.S4, V26.S4

	VADD    V5.S4, V5.S4, V24.S4
	VADD    V12.S4, V12.S4, V5.S4

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
