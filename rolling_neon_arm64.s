// NEON checksum: 32B/iter, raw sums (CHAR_OFFSET post-correction in Go).
// WORD encodings verified by GNU aarch64-as (2026-07-27).
// Go 1.27 (Aug 2026) will add these mnemonics via go.dev/issue/78498.
//
// ⚠️  CRITICAL: Never use BEQ/BNE after SUB inside a NEON-heavy loop.
//     NEON instructions (VADD, VADDP, VUADDLP, VUMULL, etc.) corrupt
//     the ARM condition flags (NZCV) on QEMU and possibly some hardware.
//     Use CBNZ/CBZ (compare-and-branch on register) instead — these
//     check the register value directly, bypassing the flags.
//     This bug took 2 days and 20+ QEMU cycles to find.  DO NOT REGRESS.
//
// Structure matches rolling_sse2_amd64.s — preload first block,
// loop N times processing preloaded/loaded block, exit with N correction.

#include "textflag.h"

// func checksum1NEON(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0
	MOVD    data_len+8(FP), R1
	CMP     $32, R1
	BLT     bail

	MOVD    s1+24(FP), R2
	MOVD    s2+32(FP), R3

	// Load weight tables
	MOVD    $w32_neon<>(SB), R4
	VLD1    (R4), [V18.B16]     // V18 = weights [32..17]
	ADD     $16, R4, R4
	VLD1    (R4), [V19.B16]     // V19 = weights [16..1]

	// Save initial values
	MOVWU   (R2), R5            // R5 = init_s1
	MOVWU   (R3), R6            // R6 = init_s2

	// Zero accumulators
	VEOR    V12.B16, V12.B16, V12.B16
	VEOR    V4.B16, V4.B16, V4.B16
	VEOR    V14.B16, V14.B16, V14.B16

	// Preload first 32B
	VLD1    (R0), [V2.B16]
	ADD     $16, R0, R11
	VLD1    (R11), [V3.B16]

	AND     $~31, R1, R7        // len & ~31
	LSR     $5, R7, R7          // N = iterations = len/32
	MOVD    R7, R12             // save N for exit correction
	ADD     $32, R0             // advance ptr past preloaded block

loop:
	// s1: byte sum -> 4xint32 delta_s1
	WORD    $0x6E202840         // VUADDLP V0.8H, V2.16B
	WORD    $0x6E202861         // VUADDLP V1.8H, V3.16B
	VADD    V0.H8, V0.H8, V1.H8
	WORD    $0x4E602800         // VSADDLP V0.4S, V0.8H

	// s2: accumulate s1_before
	VADD    V4.S4, V4.S4, V14.S4

	// s2 weighted: first half x weights [32..17]
	WORD    $0x2E32C045         // VUMULL  V5.8H, V2.8B, V18.8B
	WORD    $0x6E32C046         // VUMULL2 V6.8H, V2.16B, V18.16B
	VADDP   V5.H8, V5.H8, V6.H8
	WORD    $0x4E6028A5         // VSADDLP V5.4S, V5.8H

	// s2 weighted: second half x weights [16..1]
	WORD    $0x2E33C067         // VUMULL  V7.8H, V3.8B, V19.8B
	WORD    $0x6E33C068         // VUMULL2 V8.8H, V3.16B, V19.16B
	VADDP   V7.H8, V7.H8, V8.H8
	WORD    $0x4E6028E7         // VSADDLP V7.4S, V7.8H

	// Merge and accumulate
	VADD    V5.S4, V5.S4, V7.S4
	VADD    V12.S4, V12.S4, V5.S4

	// s1 update
	VADD    V14.S4, V14.S4, V0.S4

	// Next block — CBNZ is MANDATORY (not BEQ! NEON clobbers flags)
	SUB     $1, R7, R7
	CBNZ    R7, load_next
	B       done
load_next:
	VLD1    (R0), [V2.B16]
	ADD     $16, R0, R11
	VLD1    (R11), [V3.B16]
	ADD     $32, R0
	B       loop

done:
	// Reduce V14 -> s1
	VADDP   V0.S4, V14.S4, V14.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R8
	ADD     R5, R8, R8          // s1 = byte_sum + init_s1

	// Reduce V4 (sum s1_before)
	VADDP   V0.S4, V4.S4, V4.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R9
	LSL     $5, R9, R9          // R9 = 32 * sum(s1_before)

	// s2 correction: 32 * N * init_s1
	MULW    R12, R5, R10
	LSL     $5, R10, R10
	ADD     R10, R9, R9

	// Reduce V12 (sum weighted)
	VADDP   V0.S4, V12.S4, V12.S4
	VADDP   V0.S4, V0.S4, V0.S4
	VMOV    V0.S[0], R10
	ADD     R9, R10, R10
	ADD     R6, R10, R10        // s2 += init_s2

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
