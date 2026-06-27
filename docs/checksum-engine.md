# go-rsync Checksum Engine

> Originally developed for [Shuttle](https://github.com/henryborner/shuttle), now a standalone library.
>
> The rolling checksum algorithm follows the well-known rsync formula (CHAR_OFFSET, uint32 natural-overflow). The VPMADDWD pair-sum approach, deferred-reduction structure, and Go Plan 9 assembly implementations are original work.

## Contents

- [1. Overview](#1-overview)
- [2. Algorithm](#2-algorithm)
- [3. Loop Structure](#3-loop-structure)
- [4. Go Plan 9 Assembly Notes](#4-go-plan-9-assembly-notes)
- [5. Evolution History](#5-evolution-history)
- [6. Known Bugs and Fixes](#6-known-bugs-and-fixes)
- [7. Register Map](#7-register-map)
- [8. Test Coverage](#8-test-coverage)
- [9. Performance Data](#9-performance-data)
- [10. Appendix: SSE2 Path](#10-appendix-sse2-path)
- [11. Appendix: Per-Size Performance Data](#11-appendix-per-size-performance-data)

## 1. Overview

| Feature | go-rsync |
|---------|-----------|
| Data type | `uint8` (0..255) |
| CHAR_OFFSET | 31 (stronger than rsync's default 0, but incompatible with standard rsync. `Checksum1` handled in asm; private `checksum1` corrected in Go layer afterwards) |
| Return format | `Checksum1` returns packed `uint32`; `checksum1` returns two `uint32` scalars |
| s1 reduction | VPMADDWD pair-sum (full 32-bit) |
| s2 weighted reduction | VPMADDWD pair-sum per half (full 32-bit), no VPUNPCK |
| PREFETCHT0 | Yes (384-byte ahead prefetch) |
| Loop instructions | **19** (down 1 from v4's 20) |

> **Core idea**: Both s1 and s2 use VPMADDWD for pair-sum →one instruction multiplies adjacent int16 pairs by 1 and sums them. s2 values per half-YMM never exceed 32767 (max: 64×255+63×255=32,385), no VPADDW merge needed; each half uses VPMADDWD separately then merged as int32. Both paths use deferred reduction.

## 2. Algorithm

### 2.1 Per-Block Breakdown (64 bytes per iteration)

Block k (0-indexed):

```
s1_before_k       = cumulative s1 at start of block k
delta_s1_k        = sum of all bytes in block k             (VPMADDUBSW→VPADDW→VPMADDWD)
weighted_sum_k    = Σ (64−i)×byte_i in block k              (VPMADDUBSW→VPMADDWD per half)
s1_after_k        = s1_before_k + delta_s1_k

s1 = Σ delta_s1_k                                           (Y14)
s2 = 64 × Σ s1_before_k + Σ weighted_sum_k                  (Y4 = Σs1_before, Y12 = Σweighted)
```

**s1 reduction** uses VPMADDWD with int16 all-ones constant (Y11):

```
VPMADDUBSW →VPADDW (merge two halves) →VPMADDWD × int16_ones →8×int32 delta_s1
```

One instruction replaces VPUNPCKLWD + VPUNPCKHWD + VPADDD (3→). This works because byte sums never exceed signed int16 range (<32767).

**s2 weighted reduction** uses VPMADDWD per half (same trick as s1). int16 values per half < 32767, so VPMADDWD pair-sum is safe. No VPADDW merge needed →each half VPMADDWD separately then merged as int32.

### 2.2 Initial Value Exit Correction

`Y14` tracks only raw byte sum (init_s1 not broadcast):

```
s1 = reduce(Y14) + init_s1
s2 = 64 × [reduce(Y4) + N × init_s1] + reduce(Y12) + init_s2
```

`N` = number of 64B blocks. `init_s1` and `init_s2` read from caller pointers.

### 2.3 CHAR_OFFSET Post-Correction (Go layer)

Asm computes raw byte sum. Go adds CHAR_OFFSET afterwards (`rolling_fast_amd64.go`):

```go
// private checksum1: asm raw sum + Go CHAR_OFFSET
s1 += uint32(n) * CHAR_OFFSET
s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET

// public Checksum1: CHAR_OFFSET handled in asm (checksum1PackedAVX2)
```

### 2.4 Remaining Bytes

Asm handles all bytes →full 64B blocks and scalar remainder (0..63 bytes) processed in a byte-by-byte loop after the main loop. Go side needs no further remainder handling.

## 3. Loop Structure

(19 instructions, interleaved VPMADDUBSW)

```asm
loop:
    ; s1: VPMADDUBSW ×2 halves →VPADDW merge →VPMADDWD pair-sum
    VPMADDUBSW  Y15, Y2, Y0        ; first 32B →16 int16
    VPMADDUBSW  Y15, Y8, Y6        ; second 32B →16 int16
    VPADDW      Y6, Y0, Y0         ; merge two halves (16-bit)
    VPMADDWD    Y11, Y0, Y0        ; pair-sum →8×int32 delta_s1

    ; s2: Y4 += Y14 (s1_before accumulation →deferred)
    VPADDD      Y4, Y14, Y4

    ; s2: VPMADDUBSW × weight table →VPMADDWD per half →merge as int32
    VPMADDUBSW  Y7, Y2, Y2         ; first 32B × [64..33]
    VPMADDUBSW  Y13, Y8, Y3        ; second 32B × [32..1]
    VPMADDWD    Y11, Y2, Y2        ; first half →8 int32 pair-sums
    VPMADDWD    Y11, Y3, Y3        ; second half →8 int32
    VPADDD      Y3, Y2, Y2         ; merge two halves (32-bit)
    VPADDD      Y12, Y2, Y12       ; Y12 += weighted_sum

    PREFETCHT0  384(DI)            ; prefetch 6 cachelines ahead

    ; s1: Y14 += delta
    VPADDD      Y14, Y0, Y14

    ; load next block (check first then load, avoids overread)
    SUBQ  $1, SI
    JZ    done
    VMOVDQU  0(DI), Y2             ; next first 32B
    VMOVDQU  32(DI), Y8            ; next second 32B
    ADDQ  $64, DI
    JMP   loop
done:
```

**Key design decisions:**

- **Both s1 and s2 use VPMADDWD**: int16 values per half < 32767 (s2 max: 64×255+63×255=32,385). No VPUNPCK needed anywhere.
- **Interleaved VPMADDUBSW**: s1 issued first, s2 follows →avoids 4 instructions contending for ports 0/5 simultaneously.
- **PREFETCHT0**: ~3% gain on Xeon cloud VM, zero cost on Zen 4. Kept for older CPU compatibility.
- **Bottom load + guard**: `SUBQ/JZ` checks before load, prevents overread on last iteration.
- **Merged exit reduction**: Y4×64 + Y12 merged then one horizontal sum (saves ~5 instructions vs two separate reductions).

## 4. Go Plan 9 Assembly Notes

### 4.1 VPMADDUBSW Operand Swap

| Source | src1 role | src2 role |
|--------|-----------|-----------|
| Intel manual | **unsigned** | **signed** |
| Go Plan 9 asm | **signed** | **unsigned** |

Verified by diagnosis: `VPMADDUBSW data, ones` →data treated as signed; `VPMADDUBSW ones, data` →data treated as unsigned.

This project's usage: `VPMADDUBSW Y15(ones=+1 signed), data(unsigned), dst` →correct unsigned summation.

### 4.2 VPUNPCKLWD / VPUNPCKHWD Lane Behavior (historical →no longer used in AVX2 path)

SSE2 path still uses VPUNPCK for width extension. In the AVX2 path, VPMADDWD has replaced VPUNPCK for both s1 and s2.

- `VPUNPCKLWD Y5(zero), Y0, Y3` →zero-extends the 8 even-indexed int16 values from 16 int16 to 8 int32, operates across two 128-bit lanes without VEXTRACTI128.
- `VPUNPCKHWD Y5(zero), Y0, Y0` →zero-extends the 8 odd-indexed values to 8 int32.

The pair (8+8=16) covers all 16 int16 results from VPMADDUBSW.

### 4.3 XMM/YMM Register Aliasing

`X0` is the lower 128 bits of `Y0`, not an independent register. Writing `Y0` automatically updates `X0`. Exit reduction exploits this →no `VEXTRACTI128 $0, Y0, X0` needed.

### 4.4 VPANDN / VPTERNLOGD Operand Swap (MD5 core)

Go Plan 9 swaps src1/src2 for all non-commutative SIMD instructions:

| Instruction | Intel manual | Go Plan 9 asm |
|------------|-------------|---------------|
| `VPANDN A,B,C` | `C = ~A & B` | `C = A &^ B` (A & ~B) |
| `VPTERNLOGD imm,A,B,C` | n = (C<<2)\|(A<<1)\|B | n = (C<<2)\|(B<<1)\|A →**swapped** |

`VPTERNLOGD` truth-table immediates must be computed with Go's swapped ordering. Using Intel manual values ($0xB8/$0xCA/$0x65) produces wrong MD5 hashes. Correct Go-swapped values: R1=$0xD8, R2=$0xAC, R4=$0x63. See corrected generators in `gen_md5x8/main.go` and `gen_md5x16/main.go`.

### 4.5 Go Assembler Limitations

- VPMADDUBSW does not support memory operands (src2 must be register).
- `VPBROADCASTD` is available but was a bug source (see §6.1).
- Weight tables must use `DATA /8` with little-endian uint64 encoding.

## 5. Evolution History

| Version | Key Changes | Loop Instructions | Xeon 1KB | Ryzen 64KB |
|---------|-----------|:-----------:|:--------:|:----------:|
| v0 (baseline) | Signed VPMADDUBSW + VPMOVSXWD + per-iteration s1 reduction | 45 | →| →|
| →| Unsigned + VPUNPCK zero-extension | 41 | →| →|
| →| Preload low-weight table Y13 | 36 | →| →|
| →| s1 deferred reduction | 27 | →| →|
| v1 | Bottom load eliminates Y9/Y10 + VPBROADCASTD fix | 28 | 27.2 GB/s | 51.5 GB/s |
| v2 | VPADDW merge-first-then-extend (saves 6 instructions) | 22 | 35.8 GB/s | 64.1 GB/s |
| v3 | PREFETCHT0 + overread guard (safe bottom load) | 22 | 36.6 GB/s | 59.6 GB/s |
| **v4** | **s1 uses VPMADDWD pair-sum** (saves 2 instructions) | **20** | **→* | **69.2 GB/s** |
| v5 | **s2 per-half VPMADDWD** + asm scalar remainder + merged exit reduction | **19** | 35.1 GB/s | →|
| **v6** | **CHAR_OFFSET + packing in asm** (`checksum1PackedAVX2`), merged ones tables | **19** | **37.4 GB/s** | →|

**Cumulative**: 28→9 instructions (→2%), Xeon 1KB throughput +38%. 64KB gap vs rsync within 1.4%.

> **VPSRLD dead end**: tried using VPSRLD for packed reduction (3→ instructions). High 16 bits had garbage data causing s1 amplified 32768×. Rejected →`Roll()` requires full 32-bit correctness.

## 6. Known Bugs and Fixes

### 6.1 VPBROADCASTD Amplification (v0.1.x)

`VPBROADCASTD X0, Y14` broadcast init_s1 to 8 lanes. Each iteration `Y4 += Y14` counted it 8×. Fix: keep Y14 zero-initialized (tracking only raw byte sum), apply init_s1/s2 as scalars at exit (§2.2).

### 6.2 Y15 Register Contamination (v0.1.3)

s2 weight load segment used `LEAQ mul_T2<>+32(SB), AX; VMOVDQU (AX), Y15`, corrupting the all-ones constant table. Fix: use separate Y13 to load low weights.

### 6.3 VPANDN Operand Swap →AVX2 MD5 (v0.1.4.2)

Go Plan 9's `VPANDN A,B,C` = `C = A &^ B`, not Intel's `C = ~A & B`. MD5 code generator (`gen_md5x8/main.go`) used Intel semantics for Round 1/2/4, causing all F functions to be wrong. AVX2 MD5's 8 lanes silently miscomputed every block.

**Fix**: swap operands in generator →regenerated `md5x8_amd64.s`. Added `TestMD5x8_AVX2_Parity` (AVX2 vs stdlib md5.Sum).

### 6.4 VPGATHERDD Mask Zeroing + Go asm VSIB Bug (v0.1.4.2–v0.1.4.3)

Two bugs in the gather load path:
1. **Mask zeroing** →VPGATHERDD zeros the mask register after execution (Intel spec). Initializing the mask only once →only the first gather works.
2. **VSIB encoding** →Go Plan 9 assembler has hardcoded base register and displacement errors for VPGATHERDD encoding.

**Fix**: reload mask before each gather (`VPCMPEQD` / `KXNORW`). Bypass Go asm VSIB bug entirely with raw machine code `BYTE` opcodes.

### 6.5 VPTERNLOGD Operand Swap →AVX-512 MD5 (v0.1.4.4)

Same category as §6.3: Go Plan 9 swaps src1/src2 for `VPTERNLOGD`. Truth-table index is `n=(dst<<2)|(src2<<1)|src1` (not Intel's src1/src2 order). The three rounds using VPTERNLOGD in the AVX-512 core (R1, R2, R4) all had wrong immediates.

**Impact**: 1GB identical-file sync took 2+ minutes →server (Xeon, AVX-512) generated wrong MD5 signatures, client (stdlib MD5) found zero matches →byte-by-byte scan of 1 billion positions.

**Fix**: recomputed all imm8 values in Go-swapped order ($0xD8/$0xAC/$0x63), regenerated `md5x16_amd64.s`. Added `TestMD5x16_AVX512_Parity`, `TestMD5x16_CoreOnly`, `TestMD5x16_GatherVerification`.

## 7. Register Map

| Register | Purpose | Lifetime |
|----------|---------|----------|
| Y15 | all-ones table (0x01 × 32) for VPMADDUBSW | constant |
| Y11 | int16 all-ones (0x0001 × 16) for VPMADDWD | constant |
| Y7 | weight table [64..33] | constant |
| Y13 | weight table [32..1] | constant |
| Y2 | current 64B block, first 32B | per iteration |
| Y8 | current 64B block, second 32B | per iteration |
| Y0 | temp (s1 delta via VPMADDWD) | per iteration |
| Y3 | s2 second half | per iteration |
| Y6 | temp (s1/s2 second half) | per iteration |
| Y14 | accumulated s1 (vector, raw byte sum only) | across iterations |
| Y4 | Σ s1_before_k (deferred s2) | across iterations |
| Y12 | Σ weighted byte sum (deferred s2) | across iterations |
| DI | data pointer | across iterations |
| SI | iteration counter | across iterations |
| R13 | init_s1 (used at exit) | function lifetime |
| DX | init_s2 (used at exit) | function lifetime |
| R15 | original_len (for remainder handling) | function lifetime |
| R12 | N = iteration count (for exit correction) | function lifetime |
| R10 | exit: s1 scalar | exit only |
| R9, R11 | exit: s2 reduction temps | exit only |

Unused YMM registers: Y1, Y5, Y9, Y10.

## 8. Test Coverage

### Checksum Parity Tests (`avx2_test.go`)

Compare AVX2 and SSE2 output against byte-by-byte reference (no CHAR_OFFSET):

| Test | Data | Purpose |
|------|------|---------|
| `TestAVX2Parity` (11 groups) | zeros, 0xFF, incrementing, random | Verify AVX2 engine |
| `TestSSE2Parity` (10 groups) | zeros, 0xFF, incrementing, random | Verify SSE2 engine |

### MD5 SIMD Parity Tests (`md5x8_test.go`)

Compare AVX2/AVX-512 MD5 output against stdlib `md5.Sum`:

| Test | Scope |
|------|-------|
| `TestMD5x8_AVX2_Parity` | 8-lane AVX2 MD5 vs stdlib (700-byte blocks) |
| `TestMD5x16_AVX512_Parity` | 16-lane AVX-512 MD5 vs stdlib (2048-byte blocks) |
| `TestMD5x16_UnevenLengths` | 16 uneven-length blocks (63→096 bytes) |
| `TestMD5x16_CoreOnly` | AVX-512 core + manually constructed x matrix (bypasses gather) |
| `TestMD5x16_GatherVerification` | Verify VPGATHERDD loads correct transposed data |

### Performance Benchmarks (`tier_bench_test.go`)

Three-way comparison:

| Benchmark | Engine |
|-----------|--------|
| `SSE2/*KB` | `checksum1SSE2` (32B/iter, XMM) |
| `AVX2/*KB` | `checksum1AVX2` (64B/iter, YMM) |
| `Go/*KB` | Pure Go 128B batches (no SIMD) |

### Integration Tests (`delta_test.go`)

End-to-end delta round-trip, identical files, example usage.

## 9. Performance Data

**Intel Xeon Platinum Cloud VM (2 vCPU, ~2.5 GHz):**

| Block Size | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------------|:-----------:|:-----------:|:----------:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 8 KB | **42.8 GB/s** | →| 48.3 GB/s |
| 64 KB | **43.7 GB/s** | 26.7 GB/s | 44.3 GB/s |
| 1 MB | **43.6 GB/s** | 42.4 GB/s | →|

**AMD Ryzen 9 8940HX (Zen 4, laptop):**

| Block Size | go-rsync v6 | v1 (baseline) | Improvement |
|------------|:-----------:|:-------------:|:-----------:|
| 1 KB | 55.1 GB/s | 44.8 GB/s | +23% |
| 64 KB | **69.2 GB/s** | 51.5 GB/s | **+34%** |
| 1 MB | 51.2 GB/s | 51.2 GB/s | →|

**Cross-Platform Three-Tier Comparison (Ryzen 9, 64KB):**

| Tier | Throughput | vs AVX2 |
|------|:----------:|:-------:|
| AVX2 (64B/iter) | 69.2 GB/s | →|
| SSE2 (32B/iter) | 26.1 GB/s | 2.7× slower |
| Pure Go (128B batch) | 1.9 GB/s | 36× slower |

## 10. Appendix: SSE2 Path

The SSE2 path is not a simple mechanical translation of AVX2. Key differences:

| Aspect | AVX2 | SSE2 | Reason |
|--------|------|------|--------|
| s1 reduction | VPMADDWD pair-sum | **VPHADDW** pair-sum | Go asm lacks XMM variant of `VPADDW`; VPHADDW is the only available XMM word add |
| s2 reduction | VPADDW merge + VPUNPCK | VPUNPCK per-half (no merge) | XMM cannot use VPADDW; each half extended separately |
| Block size | 64B/iter | 32B/iter | XMM = 128-bit |
| `VPMADDWD` | YMM, int16_ones table | Not used | VPMADDWD available but cannot merge first |
| `PREFETCHT0` | 384(DI) | 384(DI) | Same |
| Loop instructions | 20 | ~22 | s2 has 2 extra VPUNPCK per half |

**VPBROADCASTD bug (v0.2.1 fix)**: Original SSE2 code broadcast `init_s1` to 4 XMM lanes, causing 4× amplification. Fix: keep X14 zero-initialized, apply init_s1 as scalar at exit (same approach as AVX2).

**XMM PADDW limitation**: Go Plan 9 assembler only defines `APADDW` for YMM registers (`{APADDW, ymm, Py1, ...}`), no XMM variant. This prevents SSE2 from using the VPADDW merge-then-extend optimization, costing ~2 extra instructions per iteration.

## 11. Appendix: Per-Size Performance Data

**Test environment**: same Xeon Platinum cloud VM, same data pattern (`i*7%251`), all include full tail byte handling. go-rsync auto-dispatches to AVX2 via `Checksum1()`. Measurement error ±3%.

| Size | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------|:---:|:---:|:---:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 4 KB | →| 36.8 GB/s | 48.3 GB/s |
| 16 KB | →| 39.2 GB/s | 49.0 GB/s |
| 64 KB | **43.7 GB/s** | 40.7 GB/s | 44.3 GB/s |
| 97 KB | →| 41.1 GB/s | 44.8 GB/s |
| 128 KB | →| 41.3 GB/s | 45.1 GB/s |
| 256 KB | →| 41.5 GB/s | 45.2 GB/s |

> v6 narrows 1KB scenario gap vs rsync from v1's →0% to →4%. 64KB is now within 1.4% of rsync. Remaining 1KB gap comes from rsync's VPSRLD/VPSRLDQ + exit correction approach, which gets ~15% more port throughput on Xeon.

---

> Related docs: [MD5 SIMD Reference](md5-simd.md) · [Project README](../README.md)

---

> Related docs: [MD5 SIMD Reference](md5-simd.md) —— [Project README](../README.md)
