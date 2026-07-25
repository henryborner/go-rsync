// NEON instruction smoke test for Go ARM64 assembler.
// This file tests which SIMD instructions / element types Go asm accepts.

#include "textflag.h"

// Dummy function that tries each instruction.
// If this compiles, the instruction is supported.
TEXT ·neonSmokeTest(SB), NOSPLIT, $0-0
	// ── Known working (from stdlib) ──
	VEOR    V0.B16, V1.B16, V2.B16
	VADD    V0.S4, V1.S4, V2.S4        // .S4 32-bit element
	VADDP   V0.S4, V1.S4, V2.S4        // .S4 pairwise
	VLD1    (R0), [V0.B16, V1.B16]
	VMOV    V0.B16, V1.B16

	// ── Test: .S4 types on other instructions ──
	VSUB    V0.S4, V1.S4, V2.S4
	VSHL    V0.S4, V1.S4, V2.S4
	VORR    V0.B16, V1.B16, V2.B16

	// ── Test: .H8 element type (16-bit elements) ──
	VADD    V0.H8, V1.H8, V2.H8
	VSUB    V0.H8, V1.H8, V2.H8
	VADDP   V0.H8, V1.H8, V2.H8

	// ── Test: supported wide instructions ──
	VUADDLV V0.B16, V1
	VUXTL   V0.H8, V1.B8
	VUXTL2  V0.H8, V1.B16

	// ── Test: VUXTL widen H→S ──
	VUXTL   V0.S4, V1.H4        // 4 halfwords → 4 words
	VUXTL2  V0.S4, V1.H8        // upper 4 halfwords → 4 words

	// ── Test: emulate VUADDLP with VUXTL + VADDP ──
	VUXTL   V10.H8, V2.B8       // widen low 8 bytes → 8 halfwords
	VUXTL2  V11.H8, V2.B16      // widen high 8 bytes → 8 halfwords
	VADDP   V0.H8, V10.H8, V11.H8  // pairwise add → 8 pair-sums

	// ── Test: emulate VSADDLP with VADDP + VUXTL ──
	VADDP   V12.H8, V5.H8, V5.H8    // pairwise add 8H→8H
	VUXTL   V6.S4, V12.H4       // widen low 4 to 4S

	RET
