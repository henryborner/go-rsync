# go-rsync Checksum Engine

> The rolling checksum follows the well-known formula (CHAR_OFFSET, uint32 natural-overflow arithmetic). The VPMADDWD pair-sum reduction, deferred-accumulation structure, and Go Plan 9 assembly implementations are original work.

## Contents

- [1. Overview](#1-overview)
- [2. Algorithm](#2-algorithm)
- [3. Loop Structure](#3-loop-structure)
- [4. Exit Reduction](#4-exit-reduction)
- [5. Optimizations](#5-optimizations)
- [6. Assembly Notes](#6-assembly-notes)
- [7. Register Map](#7-register-map)
- [8. Test Coverage](#8-test-coverage)
- [9. Performance Data](#9-performance-data)
- [A. SSE2 Path](#a-sse2-path)
- [B. Per-Size Benchmarks](#b-per-size-benchmarks)

## 1. Overview

| Feature | Value |
| --------- | ------- |
| Data type | `uint8` (0..255) |
| CHAR_OFFSET | 31 (`Checksum1` in asm; `checksum1` in Go layer) |
| Return format | `Checksum1` → packed `uint32`; `checksum1` → two `uint32` scalars |
| s1 reduction | VPMADDWD pair-sum (full 32-bit) |
| s2 weighted reduction | VPMADDWD pair-sum per half (full 32-bit), no VPUNPCK |
| PREFETCHT0 | 384 bytes ahead |
| Loop instructions | 19 |

> **Key technique**: Both s1 and s2 use VPMADDWD for pair-sum — one instruction multiplies adjacent int16 pairs by 1 and sums them. s2 values per half-YMM stay below 32767 (max: 64×255+63×255=32,385), so no VPADDW merge is needed; each half goes through VPMADDWD separately then VPADDD merged as int32. Both paths use deferred reduction.

## 2. Algorithm

### 2.1 Per-Block Breakdown (64 bytes per iteration)

Block k (0-indexed):

```text
s1_before_k       =  running s1 at start of block k
delta_s1_k        =  Σ bytes in block k               (VPMADDUBSW → VPADDW → VPMADDWD)
weighted_sum_k    =  Σ (64−i)·byte_i in block k       (VPMADDUBSW → VPMADDWD per half)
s1_after_k        =  s1_before_k + delta_s1_k

s1  =  Σ delta_s1_k                                    (Y14)
s2  =  64 × Σ s1_before_k  +  Σ weighted_sum_k         (Y4 = Σs1_before,  Y12 = Σweighted)
```text

### 2.2 s1 Reduction

VPMADDWD with an int16 all-ones constant (Y11):

```text
VPMADDUBSW  →  VPADDW (merge halves)  →  VPMADDWD × int16_ones  →  8×int32 delta_s1
```text

One instruction replaces VPUNPCKLWD + VPUNPCKHWD + VPADDD (3→1). Works because byte sums stay within signed int16 range (<32767).

### 2.3 s2 Weighted Reduction

VPMADDWD per half — same technique as s1. Each half's int16 values are <32767, so VPMADDWD pair-sum is safe. No VPADDW merge; halves processed independently then merged as int32.

## 3. Loop Structure

19 instructions per iteration, interleaved VPMADDUBSW:

```asm
loop:
    ; s1: VPMADDUBSW ×2 halves → VPADDW merge → VPMADDWD pair-sum
    VPMADDUBSW  Y15, Y2, Y0        ; first 32B → 16 int16
    VPMADDUBSW  Y15, Y8, Y6        ; second 32B → 16 int16
    VPADDW      Y6, Y0, Y0         ; merge halves (16-bit)
    VPMADDWD    Y11, Y0, Y0        ; pair-sum → 8×int32 delta_s1

    ; s2: accumulate s1_before (deferred)
    VPADDD      Y4, Y14, Y4

    ; s2: weighted sum per half
    VPMADDUBSW  Y7, Y2, Y2         ; first 32B × weights [64..33]
    VPMADDUBSW  Y13, Y8, Y3        ; second 32B × weights [32..1]
    VPMADDWD    Y11, Y2, Y2        ; first half → 8 int32 pair-sums
    VPMADDWD    Y11, Y3, Y3        ; second half → 8 int32
    VPADDD      Y3, Y2, Y2         ; merge halves (32-bit)
    VPADDD      Y12, Y2, Y12       ; Y12 += weighted_sum

    PREFETCHT0  384(DI)            ; 6 cachelines ahead

    ; s1: accumulate delta
    VPADDD      Y14, Y0, Y14

    ; bottom-load next block with OOB guard
    SUBQ  $1, SI
    JZ    done
    VMOVDQU  0(DI), Y2
    VMOVDQU  32(DI), Y8
    ADDQ  $64, DI
    JMP   loop
done:
```text

**Design rationale**:

- **Interleaved VPMADDUBSW**: s1 issued first, s2 follows — avoids 4 instructions contending for ports 0 and 5 simultaneously.
- **Bottom-load with guard**: `SUBQ/JZ` prevents overread on the last iteration.
- **PREFETCHT0**: ~3% gain on Xeon cloud VMs; zero cost on Zen 4.

## 4. Exit Reduction

### 4.1 Initial Value Correction

Y14 tracks raw byte sums only (init_s1 not broadcast):

```text
s1 = reduce(Y14) + init_s1
s2 = 64 × [reduce(Y4) + N × init_s1] + reduce(Y12) + init_s2
```text

`N` = number of 64B blocks. `init_s1` and `init_s2` read from caller pointers.

### 4.2 CHAR_OFFSET Post-Correction

Asm computes raw byte sums. Go adds CHAR_OFFSET in `rolling_fast_amd64.go`:

```go
// private checksum1: raw sums + Go CHAR_OFFSET
s1 += uint32(n) * CHAR_OFFSET
s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET

// public Checksum1: CHAR_OFFSET handled in asm (checksum1PackedAVX2)
```text

### 4.3 Remainder Bytes

Asm handles all bytes — full 64B blocks plus scalar remainder (0..63 bytes) in a byte-by-byte loop after the main loop.

## 5. Optimizations

| Version | Change | Instrs | Xeon 1KB | Ryzen 64KB |
| --------- | -------- | :------: | :--------: | :----------: |
| v0 | Signed VPMADDUBSW + VPMOVSXWD + per-iter s1 reduction | 45 | — | — |
| — | Unsigned + VPUNPCK zero-extend | 41 | — | — |
| — | Preload low-weight table Y13 | 36 | — | — |
| — | Deferred s1 reduction | 27 | — | — |
| v1 | Bottom-load + avoid init_s1 broadcast | 28 | 27.2 GB/s | 51.5 GB/s |
| v2 | VPADDW merge-first-then-extend (−6 instrs) | 22 | 35.8 GB/s | 64.1 GB/s |
| v3 | PREFETCHT0 + OOB guard | 22 | 36.6 GB/s | 59.6 GB/s |
| v4 | VPMADDWD pair-sum for s1 (−2 instrs) | 20 | — | 69.2 GB/s |
| v5 | VPMADDWD per-half for s2 + asm remainder + merged exit | 19 | 35.1 GB/s | — |
| v6 | CHAR_OFFSET + packing in asm, combined ones table | 19 | 37.4 GB/s | — |

**Cumulative**: 28→19 instructions (−32%). Xeon 1KB throughput +38%.

> **Rejected optimization**: VPSRLD for packed reduction (3→2 instructions). High 16 bits contain garbage, causing s1 amplification by 32768×. `Roll()` requires full 32-bit correctness.

### 5.1 CHAR_OFFSET Post-Correction Overflow

The AVX2/SSE2 assembly paths compute raw sums (without CHAR_OFFSET), then
apply a post-correction in Go or assembly:

```text
s1 += uint32(n) * CHAR_OFFSET
s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET
```text

This correction is **not byte-identical** to the pure-Go path (which adds
CHAR_OFFSET per-byte) when `n ∈ [65536, 92681]`. In that range,
`n*(n+1) ≥ 2³²`, so the `uint32` intermediate multiplication wraps. The
per-byte accumulation hits overflow at different intermediate steps,
producing a different final `s2`.

**This is not a bug.** Both `Checksum1` (signature generation) and
`checksum1` (rolling match) use the **same** raw+correction path on any
given machine, so they remain mutually consistent. The only scenario
where the divergence matters is cross-ISA (e.g. AVX2-generated signatures
matched on a pure-Go ARM machine), which go-rsync does not do.

Verified by `TestChecksum1Parity` in `delta_test.go`.

## 6. Assembly Notes

### 6.1 VPMADDUBSW Operand Swap

| Source | src1 role | src2 role |
| -------- | ----------- | ----------- |
| Intel manual | unsigned | signed |
| Go Plan 9 asm | signed | unsigned |

Usage: `VPMADDUBSW Y15(ones=+1, signed), data(unsigned), dst` → correct unsigned sum.

### 6.2 VPANDN / VPTERNLOGD Operand Swap

Go Plan 9 swaps src1/src2 for non-commutative SIMD instructions:

| Instruction | Intel | Go Plan 9 |
| ------------ | ------- | ----------- |
| `VPANDN A,B,C` | `C = ~A & B` | `C = A &^ B` |
| `VPTERNLOGD imm,A,B,C` | n = (C<<2)\ | (A<<1)\ | B | n = (C<<2)\ | (B<<1)\ | A |

`VPTERNLOGD` truth-table immediates must use Go-swapped order. Using Intel-manual values produces wrong MD5 hashes. Correct Go values: R1=$0xD8, R2=$0xAC, R4=$0x63. See `gen_md5x8/main.go` and `gen_md5x16/main.go`.

### 6.3 XMM/YMM Register Aliasing

`X0` is the low 128 bits of `Y0`. Writing `Y0` updates `X0`. Exit reduction exploits this — no `VEXTRACTI128 $0, Y0, X0` needed.

### 6.4 Implementation Notes

- VPMADDUBSW does not accept memory operands on x86 (src2 must be register). This is an ISA-level encoding restriction, not specific to Go.
- `VPBROADCASTD`: broadcasting `init_s1` into all vector lanes would cause lane-count amplification on reduction. Use scalar init values at exit instead.
- Weight tables: use `DATA /4` for int32 lanes, `DATA /8` for int64 lanes. Mismatched element size between DATA and the consuming instruction causes lane misalignment (every-other-lane garbage). See §6.5.

### 6.5 Assembly Notes

Common pitfalls when writing SIMD assembly for Go, confirmed on
amd64 (Intel Xeon and AMD Zen 4).

**XMM vs YMM registers.**  Both XMM (128-bit) and YMM (256-bit) forms
of AVX packed-word instructions (`VPADDW`, `VPMADDWD`) are available.
The SSE2 checksum path uses XMM forms; the AVX2 path uses YMM forms.

**VPGATHERDD (VEX) syntax.** Go Plan 9 assembler uses a non-Intel operand
order: `VPGATHERDD mask, (base)(index*scale), dst`.  For AVX2 the mask
is a YMM register:

```asm
VPGATHERDD Y2, (R8)(Y7*2), Y1    // mask first, VSIB middle, dst last
```text

(The 16-way AVX-512 form `VPGATHERDD (base)(zmm*1), K1, dst`
puts the k-mask between the memory operand and the destination.)

**VPGATHERDQ (AVX-512).** Native Go asm syntax:
`VPGATHERDQ (R8)(Yidx*4), K1, Z10`.  The index register is always YMM
(8 × int32), the scale multiplies the dword index to get the byte offset.
For 4-byte-aligned data (all standard rsync block sizes), use scale=4.
For 8-byte-aligned data use scale=8.  The EVEX.W=1 bit distinguishes it
from VPGATHERDD; the Go assembler handles this automatically.

**DATA element-size mismatch.**  Go's `DATA` directive uses `/4` for
32-bit elements and `/8` for 64-bit elements.  If a table is expanded
with `/8` (e.g. `DATA /8, $1` repeated 8×) but loaded with 32-bit
instructions like `VPADDD`, each 64-bit word spans two int32 lanes:
bytes `[01 00 00 00 00 00 00 00]` become `[1, 0, 1, 0, ...]`.
**Always match DATA element size to the consuming instruction's lane
width:** `/4` for int32, `/8` for int64.

**VMOVDQA vs VMOVDQU.**  `VMOVDQA64` requires 64-byte alignment of the
memory operand; Go DATA symbols are not guaranteed to be aligned.
Always use `VMOVDQU` variants for load/store unless the symbol is
explicitly aligned.

**Stack index tables.**  When building gather-index tables on the stack,
write indices at their natural width: `MOVL` for int32 indices at
4-byte intervals (as in `md5x16_gather_amd64.s`).  Load with the
matching `VMOVDQU32`.  Using mismatched widths (e.g. `MOVQ` stores at
8-byte intervals loaded with `VMOVDQU32`) leaves garbage in alternative
lanes.

**VZEROUPPER.**  Mandatory before every `RET` in any function that
touches YMM or ZMM registers.  Missing it causes severe performance
degradation (~30%) on subsequent SSE/AVX code due to false
dependencies.

## 7. Register Map

| Register | Purpose | Lifetime |
| ---------- | --------- | ---------- |
| Y15 | all-ones table (0x01 × 32) | constant |
| Y11 | int16 all-ones (0x0001 × 16) | constant |
| Y7 | weight table [64..33] | constant |
| Y13 | weight table [32..1] | constant |
| Y2 | current 64B block, first 32B | per iteration |
| Y8 | current 64B block, second 32B | per iteration |
| Y0 | temp (s1 delta via VPMADDWD) | per iteration |
| Y3 | s2 second half | per iteration |
| Y6 | temp (s1/s2 second half) | per iteration |
| Y14 | accumulated s1 (vector, raw bytes only) | across iterations |
| Y4 | Σ s1_before_k (deferred s2) | across iterations |
| Y12 | Σ weighted byte sum (deferred s2) | across iterations |
| DI | data pointer | across iterations |
| SI | iteration counter | across iterations |
| R13 | init_s1 (exit use) | function lifetime |
| DX | init_s2 (exit use) | function lifetime |
| R15 | original_len (remainder) | function lifetime |
| R12 | N = iteration count (exit correction) | function lifetime |

## 8. Test Coverage

### Checksum Parity (`avx2_test.go`)

| Test | Data | Purpose |
| ------ | ------ | --------- |
| `TestAVX2Parity` (11 cases) | zeros, 0xFF, incremental, random | Verify AVX2 engine |
| `TestSSE2Parity` (10 cases) | zeros, 0xFF, incremental, random | Verify SSE2 engine |

### MD5 SIMD Parity (`md5x8_test.go`)

| Test | Scope |
| ------ | ------- |
| `TestMD5x8_AVX2_Parity` | 8-way AVX2 MD5 vs stdlib (700-byte blocks) |
| `TestMD5x16_AVX512_Parity` | 16-way AVX-512 MD5 vs stdlib (2048-byte blocks) |
| `TestMD5x16_UnevenLengths` | 16 mixed-size blocks (63–4096 bytes) |
| `TestMD5x16_CoreOnly` | Core with manually-built x matrix (bypasses gather) |
| `TestMD5x16_GatherVerification` | Verify gather loads correct transposed data |

### Performance (`tier_bench_test.go`)

Three-way comparison: AVX2 (64B/iter, YMM) / SSE2 (32B/iter, XMM) / Pure Go (128B batch).

### Integration (`delta_test.go`)

End-to-end delta round-trip, identical files, example usage.

## 9. Performance Data

**Intel Xeon Platinum cloud VM (2 vCPU, ~2.5 GHz):**

| Block Size | go-rsync v6 | go-rsync v4 | Reference AVX2 |
| ------------ | :-----------: | :-----------: | :--------------: |
| 1 KB | 37.4 GB/s | 16.8 GB/s | 43.4 GB/s |
| 8 KB | 42.8 GB/s | — | 48.3 GB/s |
| 64 KB | 43.7 GB/s | 26.7 GB/s | 44.3 GB/s |
| 1 MB | 43.6 GB/s | 42.4 GB/s | — |

**AMD Ryzen 9 8940HX (Zen 4, laptop):**

| Block Size | go-rsync | v1 (baseline) | Improvement |
| ------------ | :-----------: | :-------------: | :-----------: |
| 1 KB | 61.0 GB/s | 44.8 GB/s | +36% |
| 64 KB | 75.6 GB/s | 51.5 GB/s | +47% |
| 1 MB | 75.2 GB/s | 51.2 GB/s | +47% |

**Three-tier comparison (Ryzen 9, 64KB):**

| Tier | Throughput | vs AVX2 |
| ------ | :----------: | :-------: |
| AVX2 (64B/iter) | 75.6 GB/s | — |
| SSE2 (32B/iter) | 38.6 GB/s | 2.0× slower |
| Pure Go (128B batch) | 1.9 GB/s | 40× slower |

## A. SSE2 Path

SSE2 path (32B/iter via XMM registers). Uses the same
VPADDW+VPMADDWD pattern as AVX2.

| Aspect | AVX2 | SSE2 |
| -------- | ------ | ------ |
| s1 reduction | VPMADDWD pair-sum | VPADDW merge + VPMADDWD pair-sum |
| s2 reduction | VPMADDWD per-half | VPMADDWD per-half |
| Block size | 64B/iter | 32B/iter |
| Loop instructions | 19 | 16 |

## B. Per-Size Benchmarks

Same Xeon Platinum cloud VM, data pattern `i*7%251`, full tail-byte handling. Measurement error ±3%.

| Size | go-rsync v6 | go-rsync v4 | Reference AVX2 |
| ------ | :---: | :---: | :---: |
| 1 KB | 37.4 GB/s | 16.8 GB/s | 43.4 GB/s |
| 4 KB | — | 36.8 GB/s | 48.3 GB/s |
| 16 KB | — | 39.2 GB/s | 49.0 GB/s |
| 64 KB | 43.7 GB/s | 40.7 GB/s | 44.3 GB/s |
| 97 KB | — | 41.1 GB/s | 44.8 GB/s |
| 128 KB | — | 41.3 GB/s | 45.1 GB/s |
| 256 KB | — | 41.5 GB/s | 45.2 GB/s |

---

> Related: [MD5 SIMD Reference](md5-simd.md) | [Project README](../README.md)
