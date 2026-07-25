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
	VMUL    V0.S4, V1.S4, V2.S4
	VSHL    V0.S4, V1.S4, V2.S4
	VORR    V0.B16, V1.B16, V2.B16

	// ── Test: .H8 element type (16-bit elements) ──
	VADD    V0.H8, V1.H8, V2.H8
	VSUB    V0.H8, V1.H8, V2.H8
	VADDP   V0.H8, V1.H8, V2.H8

	// ── Test: long / wide instructions ──
	VUADDLP V0.H8, V1.B16
	VSADDLP V0.S4, V1.H8
	VUADDLV V0.B16, V1
	VUMULL  V0.H8, V1.B8, V2.B8
	VUMULL2 V0.H8, V1.B16, V2.B16
	VUXTL   V0.H8, V1.B8
	VUXTL2  V0.H8, V1.B16

	RET
