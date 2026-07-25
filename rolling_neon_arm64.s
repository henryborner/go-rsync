// NEON checksum: 32B/iter, raw sums (CHAR_OFFSET post-correction in Go).
// Simple first version — structure matches SSE2 path, no interleaving yet.
//
// ARM64 NEON baseline:
//   s1: VUADDLP + VADD + VSADDLP (byte pair-sum → int32 lanes)
//   s2: VUMULL + VADDP + VSADDLP (byte×weight → weighted pair-sum)
//
// No VPMADDUBSW on ARM — s2 weighted path costs 4 insns per 16B half.

#include "textflag.h"

// func checksum1NEON(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0       // R0 = data ptr
	MOVD    data_len+8(FP), R1   // R1 = len
	CMP     $32, R1
	BLT     bail

	MOVD    s1+24(FP), R2        // R2 = *s1
	MOVD    s2+32(FP), R3        // R3 = *s2

	// ── Load weight tables ──
	MOVD    $w32_neon<>(SB), R4
	VLD1    (R4), [V18.B16]      // V18 = weights [32..17]
	ADD     $16, R4, R4
	VLD1    (R4), [V19.B16]      // V19 = weights [16..1]

	// ── Save initial values (applied as scalars at exit) ──
	MOVWU   (R2), R5             // R5 = init_s1
	MOVWU   (R3), R6             // R6 = init_s2

	// ── Zero accumulators ──
	VEOR    V12.B16, V12.B16, V12.B16   // Σ weighted byte sums
	VEOR    V4.B16, V4.B16, V4.B16      // Σ s1_before_k
	VEOR    V14.B16, V14.B16, V14.B16   // running byte-sum (no init_s1)

	// Preload first 32B
	VLD1    (R0), [V2.B16]       // first 16B → V2
	VLD1    16(R0), [V3.B16]     // second 16B → V3

	AND     $~31, R1, R7         // len & ~31
	LSR     $5, R7, R7           // iterations = len/32
	MOVD    R7, R12              // R12 = N (save for exit correction)
	ADD     $32, R0              // advance ptr past first block

loop:
	// ═══════════════════════════════════════
	// s1: byte sum → 4×int32 delta_s1
	// ═══════════════════════════════════════
	VUADDLP V0.8H, V2.16B          // first 16B → 8×uint16 pair-sums
	VUADDLP V1.8H, V3.16B          // second 16B → 8×uint16 pair-sums
	VADD    V0.8H, V0.8H, V1.8H    // merge halves → 8×uint16 (each = 4-byte sum)
	VSADDLP V0.4S, V0.8H           // 8×uint16 → 4×int32 delta_s1 (each = 8-byte sum)

	// s2: accumulate s1_before
	VADD    V4.4S, V4.4S, V14.4S

	// ═══════════════════════════════════════
	// s2 weighted: first half × weights [32..17]
	// ═══════════════════════════════════════
	VUMULL  V5.8H, V2.8B, V18.8B    // data[0..7] × weights[32..25] → 8×uint16
	VUMULL2 V6.8H, V2.16B, V18.16B  // data[8..15] × weights[24..17] → 8×uint16
	VADDP   V5.8H, V5.8H, V6.8H     // pairwise add → 8×uint16 pair-sums
	VSADDLP V5.4S, V5.8H            // pairwise add-long → 4×int32

	// ═══════════════════════════════════════
	// s2 weighted: second half × weights [16..1]
	// ═══════════════════════════════════════
	VUMULL  V7.8H, V3.8B, V19.8B
	VUMULL2 V8.8H, V3.16B, V19.16B
	VADDP   V7.8H, V7.8H, V8.8H
	VSADDLP V7.4S, V7.8H

	// Merge halves and accumulate
	VADD    V5.4S, V5.4S, V7.4S
	VADD    V12.4S, V12.4S, V5.4S

	// Prefetch 6 cachelines ahead (384 bytes)
	PRFM    PLDL1KEEP, 384(R0)

	// s1 update: running s1 += delta_s1
	VADD    V14.4S, V14.4S, V0.4S

	// Next block
	SUB     $1, R7, R7
	BEQ     done
	VLD1    (R0), [V2.B16]
	VLD1    16(R0), [V3.B16]
	ADD     $32, R0
	B       loop

done:
	// ═══════════════════════════════════════
	// Exit: reduce V14 → s1,  V4|V12 → s2
	// ═══════════════════════════════════════

	// Reduce V14 → s1
	VADDP   V0.4S, V14.4S, V14.4S  // [a+b, c+d, a+b, c+d]
	VADDP   V0.4S, V0.4S, V0.4S    // [a+b+c+d, ...]
	UMOV    R8, V0.S[0]
	ADD     R5, R8, R8              // s1 = byte_sum + init_s1

	// Reduce V4 (Σ s1_before)
	VADDP   V0.4S, V4.4S, V4.4S
	VADDP   V0.4S, V0.4S, V0.4S
	UMOV    R9, V0.S[0]
	LSL     $5, R9, R9              // R9 = 32 × Σ s1_before

	// s2 correction: 32 × N × init_s1
	MULW    R12, R5, R10            // R10 = init_s1 × N (32-bit multiply)
	LSL     $5, R10, R10            // R10 = 32 × N × init_s1
	ADD     R10, R9, R9             // R9 = 32×(Σ s1_before + N×init_s1)

	// Reduce V12 (Σ weighted)
	VADDP   V0.4S, V12.4S, V12.4S
	VADDP   V0.4S, V0.4S, V0.4S
	UMOV    R10, V0.S[0]
	ADD     R9, R10, R10            // R10 = Σ weighted + correction
	ADD     R6, R10, R10            // R10 += init_s2

	MOVW       R8, (R2)          // store s1
	MOVW       R10, (R3)         // store s2

	MOVB       $1, ret+40(FP)
	RET

bail:
	MOVB       $0, ret+40(FP)
	RET

// ── Weight table: 32 descending bytes [32,31,...,1] as LE uint64 ──
// First 16B: [32..17], second 16B: [16..1]
DATA w32_neon<>+0(SB)/8,  $0x191a1b1c1d1e1f20  // 32,31,30,29,28,27,26,25
DATA w32_neon<>+8(SB)/8,  $0x1112131415161718  // 24,23,22,21,20,19,18,17
DATA w32_neon<>+16(SB)/8, $0x090a0b0c0d0e0f10  // 16,15,14,13,12,11,10, 9
DATA w32_neon<>+24(SB)/8, $0x0102030405060708  //  8, 7, 6, 5, 4, 3, 2, 1
GLOBL w32_neon<>(SB), RODATA|NOPTR, $32
