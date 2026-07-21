// AVX2 checksum: 64B/iter, VPMADDWD pair-sum, deferred reduction.
// CHAR_OFFSET post-correction in Go.
//
// ⚠️  IMPORTANT — Go Plan 9 asm operand swap:
//   Intel manual:  VPMADDUBSW(unsigned src1,  signed src2)
//   Go Plan 9 asm: VPMADDUBSW( signed src1, unsigned src2)  ← SWAPPED!
//   Our usage:     VPMADDUBSW Y_ones(+1 signed), Y_data(unsigned), Y_dst
//   → data bytes treated as unsigned (0..255), ones as signed +1. ✅
//   Do NOT swap the operands or 0xFF will be misinterpreted as -1.
//
//   This is verified by parity tests: 64 bytes of 0xFF → s1=16320 (correct).

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI        // buf ptr
	MOVQ    data_len+8(FP), SI    // len
	CMPQ    SI, $64               // need at least 64 bytes
	JL      bail

	MOVQ    s1+24(FP), CX         // *ps1
	MOVQ    s2+32(FP), R8         // *ps2

	// ── Tables (ones + int16_ones are one 64B symbol) ──
	LEAQ    ones<>+0(SB), AX
	VMOVDQU (AX), Y15             // byte ones (0x01 × 32) for VPMADDUBSW
	VMOVDQU 32(AX), Y11           // int16 ones (0x0001 × 16) for VPMADDWD
	LEAQ    mul_T2<>+0(SB), AX
	VMOVDQU (AX), Y7              // weights [64..33]
	VMOVDQU 32(AX), Y13           // weights [32..1]

	// ── Save initial values (applied as scalars at exit) ──
	MOVL    (CX), R13             // R13 = init_s1
	MOVL    (R8), DX              // DX  = init_s2

	// ── Zero accumulators ──
	VPXOR   Y12, Y12, Y12         // Σ weighted byte sums (deferred)
	VPXOR   Y4, Y4, Y4            // Y4 = Σ s1_before_k  (deferred s2)
	VPXOR   Y14, Y14, Y14         // Y14 = running byte-sum (vector, no init_s1)

	// Preload first 64B block
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y8
	MOVQ    SI, R15               // R15 = original_len (save for remainder)
	ANDQ    $~63, SI              // len & ~63
	SHRQ    $6, SI                // iterations = len/64
	MOVQ    SI, R12               // R12 = N (for exit correction)
	ADDQ    $64, DI

loop:
	// ═══════════════════════════════════════
	// s1 VPMADDUBSW first — finishes ~5c later, consumed immediately.
	// s2 VPMADDUBSW follows — staggered port 0/5 usage.
	// ═══════════════════════════════════════

	VPMADDUBSW Y15, Y2, Y0        // s1: first 32B → 16 int16
	VPMADDUBSW Y15, Y8, Y6        // s1: second 32B → 16 int16
	VPADDW  Y6, Y0, Y0            // s1 merge halves (16-bit)
	VPMADDWD Y11, Y0, Y0          // s1 pair-sum → 8×int32 delta_s1

	// ═══════════════════════════════════════
	// s2: s1_before (independent, port 0/1/5)
	// ═══════════════════════════════════════
	VPADDD  Y4, Y14, Y4           // Y4 = Σ running_s1_at_block_start

	// ═══════════════════════════════════════
	// s2 VPMADDUBSW — staggered after s1, port 0/5 now free from s1's VPMADDWD.
	// Y2 consumed (=overwritten), Y8 consumed → Y3.
	// ═══════════════════════════════════════
	VPMADDUBSW Y7, Y2, Y2         // s2: first 32B × weights → 16 int16
	VPMADDUBSW Y13, Y8, Y3        // s2: second 32B × weights → Y3

	// ═══════════════════════════════════════
	// s2: VPMADDWD pair-sums (staggered after s1 VPMADDWD)
	// ═══════════════════════════════════════
	VPMADDWD Y11, Y2, Y2          // first half → 8 int32 pair-sums
	VPMADDWD Y11, Y3, Y3          // second half → 8 int32
	VPADDD  Y3, Y2, Y2            // merge halves (32-bit)
	VPADDD  Y12, Y2, Y12          // Y12 += weighted_sum

	// Prefetch 6 cachelines ahead (384 bytes).
	PREFETCHT0 384(DI)

	// ═══════════════════════════════════════
	// s1: accumulate delta → running s1 (vector)
	// ═══════════════════════════════════════
	VPADDD  Y14, Y0, Y14          // running s1 += delta

	// ── Load next block (check before load to avoid OOB) ──
	SUBQ    $1, SI
	JZ      done
	VMOVDQU 0(DI), Y2             // next first 32B → Y2
	VMOVDQU 32(DI), Y8            // next second 32B → Y8
	ADDQ    $64, DI
	JMP     loop

done:
	// ═══════════════════════════════════════
	// Exit: reduce Y14 → s1,  Y4|Y12 → s2
	// ═══════════════════════════════════════

	// s1 = reduce(Y14)
	VEXTRACTI128 $1, Y14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $8, X14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $4, X14, X1
	VPADDD  X1, X14, X14
	VMOVD   X14, R10
	ADDL    R13, R10               // s1 = byte_sum + init_s1

	// s2: merge 64×Y4 + Y12 before reduction (saves one reduction pass).
	// VPSLLD scales each lane by 64; VPADDD merges weighted sums.
	VPSLLD  $6, Y4, Y4             // Y4 = 64 × Σ s1_before (per lane)
	VPADDD  Y12, Y4, Y4            // Y4 = 64·Σs1_before + Σweighted
	VEXTRACTI128 $1, Y4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $8, X4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $4, X4, X1
	VPADDD  X1, X4, X4
	VMOVD   X4, R11

	// s2 correction: N × 64 × init_s1 + init_s2
	MOVL    R12, R9                // R9 = N
	IMULL   R13, R9                // R9 = N × init_s1
	SHLL    $6, R9                 // R9 = 64 × N × init_s1
	ADDL    R9, R11                // s2 += 64·N·init_s1
	ADDL    DX, R11                // s2 += init_s2

	// ═══ Scalar remainder (0..63 bytes) ═══
	MOVQ    R15, AX                // AX = original_len
	ANDQ    $63, AX                // remainder count
	JZ      skip_rem
	MOVQ    AX, SI                 // SI = counter
rem_loop:
	MOVBQZX (DI), R14              // byte value (zero-extend)
	ADDL    R14, R10               // s1 += byte
	ADDL    R10, R11               // s2 += s1
	ADDQ    $1, DI
	DECQ    SI
	JNZ     rem_loop
skip_rem:

	MOVL    R10, (CX)              // store s1
	MOVL    R11, (R8)              // store s2

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// func checksum1PackedAVX2(data []byte) uint32
// Same core as checksum1AVX2, with CHAR_OFFSET + packing in asm.
// init_s1=0, init_s2=0 (Checksum1 always starts fresh).
TEXT ·checksum1PackedAVX2(SB), NOSPLIT, $0-28
	MOVQ    data+0(FP), DI        // buf ptr
	MOVQ    data_len+8(FP), SI    // len
	CMPQ    SI, $64
	JL      pbail

	// ── Tables ──
	LEAQ    ones<>+0(SB), AX
	VMOVDQU (AX), Y15
	VMOVDQU 32(AX), Y11
	LEAQ    mul_T2<>+0(SB), AX
	VMOVDQU (AX), Y7
	VMOVDQU 32(AX), Y13

	// ── Zero accumulators (init_s1=0, init_s2=0) ──
	VPXOR   Y12, Y12, Y12
	VPXOR   Y4, Y4, Y4
	VPXOR   Y14, Y14, Y14

	// ── Preload first 64B block ──
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y8
	MOVQ    SI, R15               // R15 = n (for remainder + CHAR_OFFSET)
	ANDQ    $~63, SI
	SHRQ    $6, SI
	ADDQ    $64, DI

ploop:
	VPMADDUBSW Y15, Y2, Y0
	VPMADDUBSW Y15, Y8, Y6
	VPADDW  Y6, Y0, Y0
	VPMADDWD Y11, Y0, Y0

	VPADDD  Y4, Y14, Y4

	VPMADDUBSW Y7, Y2, Y2
	VPMADDUBSW Y13, Y8, Y3

	VPMADDWD Y11, Y2, Y2
	VPMADDWD Y11, Y3, Y3
	VPADDD  Y3, Y2, Y2
	VPADDD  Y12, Y2, Y12

	PREFETCHT0 384(DI)
	VPADDD  Y14, Y0, Y14

	SUBQ    $1, SI
	JZ      pdone
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y8
	ADDQ    $64, DI
	JMP     ploop

pdone:
	// s1 reduction (same as original)
	VEXTRACTI128 $1, Y14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $8, X14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $4, X14, X1
	VPADDD  X1, X14, X14
	VMOVD   X14, R10              // s1 raw (init_s1=0)

	// s2 reduction (same as original)
	VPSLLD  $6, Y4, Y4
	VPADDD  Y12, Y4, Y4
	VEXTRACTI128 $1, Y4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $8, X4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $4, X4, X1
	VPADDD  X1, X4, X4
	VMOVD   X4, R11               // s2 raw (init_s2=0)

	// Scalar remainder (same as original)
	MOVQ    R15, AX
	ANDQ    $63, AX
	JZ      prem_done
	MOVQ    AX, SI
prem_loop:
	MOVBQZX (DI), R14
	ADDL    R14, R10
	ADDL    R10, R11
	ADDQ    $1, DI
	DECQ    SI
	JNZ     prem_loop
prem_done:

	// ═══ CHAR_OFFSET + packing ═══
	// s1 += n * 31
	MOVL    R15, R9
	SHLL    $5, R9
	SUBL    R15, R9
	ADDL    R9, R10

	// s2 += n*(n+1)/2 * 31
	MOVL    R15, R9
	ADDL    $1, R9
	IMULL   R15, R9
	SHRL    $1, R9
	MOVL    R9, AX
	SHLL    $5, AX
	SUBL    R9, AX
	ADDL    AX, R11

	// Pack
	ANDL    $0xFFFF, R10
	ANDL    $0xFFFF, R11
	SHLL    $16, R11
	ORL     R11, R10

	MOVL    R10, ret+24(FP)
	VZEROUPPER
	RET

pbail:
	MOVL    $0, ret+24(FP)
	RET

// ── Combined ones table: first 32B = byte-ones (0x01), next 32B = int16-ones (0x0001) ──
DATA ones<>+0(SB)/8,  $0x0101010101010101
DATA ones<>+8(SB)/8,  $0x0101010101010101
DATA ones<>+16(SB)/8, $0x0101010101010101
DATA ones<>+24(SB)/8, $0x0101010101010101
DATA ones<>+32(SB)/8, $0x0001000100010001
DATA ones<>+40(SB)/8, $0x0001000100010001
DATA ones<>+48(SB)/8, $0x0001000100010001
DATA ones<>+56(SB)/8, $0x0001000100010001
GLOBL ones<>(SB), RODATA|NOPTR, $64

// ── Byte weight table: 64 descending bytes [64,63,...,1] as LE uint64 ──
DATA mul_T2<>+0(SB)/8,  $0x393a3b3c3d3e3f40  // 64,63,62,61,60,59,58,57
DATA mul_T2<>+8(SB)/8,  $0x3132333435363738  // 56,55,54,53,52,51,50,49
DATA mul_T2<>+16(SB)/8, $0x292a2b2c2d2e2f30  // 48,47,46,45,44,43,42,41
DATA mul_T2<>+24(SB)/8, $0x2122232425262728  // 40,39,38,37,36,35,34,33
DATA mul_T2<>+32(SB)/8, $0x191a1b1c1d1e1f20  // 32,31,30,29,28,27,26,25
DATA mul_T2<>+40(SB)/8, $0x1112131415161718  // 24,23,22,21,20,19,18,17
DATA mul_T2<>+48(SB)/8, $0x090a0b0c0d0e0f10  // 16,15,14,13,12,11,10, 9
DATA mul_T2<>+56(SB)/8, $0x0102030405060708  //  8, 7, 6, 5, 4, 3, 2, 1
GLOBL mul_T2<>(SB), RODATA|NOPTR, $64
