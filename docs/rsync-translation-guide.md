# go-rsync AVX2 Checksum — Design & Translation Notes

> Originally developed for [Shuttle](https://github.com/henryborner/shuttle), now a standalone library.
>
> The checksum algorithm and deferred-reduction structure draw from studying rsync's `checksum.c` and `simd-checksum-avx2.S`. The VPMADDWD pair-sum approach and Go Plan 9 assembly adaptations are original work.

## 1. High-Level Summary

| Feature | go-rsync |
|---------|-----------|
| Data type | `uint8` (0..255) |
| CHAR_OFFSET | 31 (in-asm for `Checksum1`, post-correction for private `checksum1`) |
| Return format | `Checksum1` returns packed `uint32`; `checksum1` returns two `uint32` scalars |
| s1 reduction | VPMADDWD pair-sum (full 32-bit) |
| s2 weighted reduction | VPMADDWD pair-sum per half (full 32-bit), no VPUNPCK |
| PREFETCHT0 | yes (384 bytes ahead) |
| Loop instructions | **19** (down from 20 in v4) |

> **Key insight:** Both s1 and s2 use VPMADDWD for pair-sum — one instruction that multiplies adjacent int16 pairs by 1 and sums. s2 values fit per-half (< 32767), so no VPADDW merge needed; each half is VPMADDWD'd separately then merged as int32. Both paths use deferred reduction.

---

## 2. Algorithm (unsigned + deferred reduction + VPMADDWD)

### 2.1 Per-block breakdown (64 bytes per iteration)

Block k (0-indexed):

```
s1_before_k       = running s1 at start of block k
delta_s1_k        = Σ bytes in block k              (VPMADDUBSW→VPADDW→VPMADDWD)
weighted_sum_k    = Σ (64-i)·byte_i in block k      (VPMADDUBSW→VPMADDWD per half)
s1_after_k        = s1_before_k + delta_s1_k

s1 = Σ delta_s1_k                                    (Y14)
s2 = 64 × Σ s1_before_k + Σ weighted_sum_k           (Y4 = Σs1_before, Y12 = Σweighted)
```

**s1 reduction** uses VPMADDWD with an int16 all-1s constant (Y11):
```
VPMADDUBSW → VPADDW (merge halves) → VPMADDWD × int16_ones → 8×int32 delta_s1
```
One instruction replaces VPUNPCKLWD + VPUNPCKHWD + VPADDD (3→1). This works because byte sums fit in signed int16 (<32767).

**s2 weighted reduction** uses VPMADDWD per half (same trick as s1). Each half's int16 values stay < 32767 (max: 64×255+63×255=32,385), so VPMADDWD pair-sum is safe. No VPADDW merge needed — the two halves are VPMADDWD'd separately then merged as int32:

### 2.2 Exit correction for initial values

Since `Y14` tracks **only byte sums** (init_s1 not broadcast):

```
s1 = reduce(Y14) + init_s1
s2 = 64 × [reduce(Y4) + N × init_s1] + reduce(Y12) + init_s2
```

`N` = number of 64B blocks. `init_s1` and `init_s2` are read from caller's pointers.

### 2.3 CHAR_OFFSET post-correction (Go layer)

The ASM computes raw byte sums. Go adds CHAR_OFFSET afterward (`rolling_fast_amd64.go`):

```go
// private checksum1: asm raw sums + Go CHAR_OFFSET
s1 += uint32(n) * CHAR_OFFSET
s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET

// public Checksum1: CHAR_OFFSET in asm via checksum1PackedAVX2
```

### 2.4 Remainder bytes

ASM handles ALL bytes — full 64B blocks AND scalar remainder (0..63 bytes) in a byte-by-byte loop after the main loop. No Go-side remainder processing needed.

---

## 3. Loop Structure (19 instructions, staggered VPMADDUBSW)

```asm
loop:
    ; s1: VPMADDUBSW ×2 halves → VPADDW merge → VPMADDWD pair-sum
    VPMADDUBSW  Y15, Y2, Y0        ; first 32B → 16 int16
    VPMADDUBSW  Y15, Y8, Y6        ; second 32B → 16 int16
    VPADDW      Y6, Y0, Y0         ; merge halves (16-bit)
    VPMADDWD    Y11, Y0, Y0        ; pair-sum → 8×int32 delta_s1

    ; s2: Y4 += Y14 (s1_before accumulation — deferred)
    VPADDD      Y4, Y14, Y4

    ; s2: VPMADDUBSW × weight tables → VPMADDWD per half → merge as int32
    VPMADDUBSW  Y7, Y2, Y2         ; first 32B × [64..33]
    VPMADDUBSW  Y13, Y8, Y3        ; second 32B × [32..1]
    VPMADDWD    Y11, Y2, Y2        ; first half → 8 int32 pair-sums
    VPMADDWD    Y11, Y3, Y3        ; second half → 8 int32
    VPADDD      Y3, Y2, Y2         ; merge halves (32-bit)
    VPADDD      Y12, Y2, Y12       ; Y12 += weighted_sum

    PREFETCHT0  384(DI)            ; prefetch 6 cachelines ahead

    ; s1: Y14 += delta
    VPADDD      Y14, Y0, Y14

    ; load next block (check before load to avoid OOB)
    SUBQ  $1, SI
    JZ    done
    VMOVDQU  0(DI), Y2             ; next first 32B
    VMOVDQU  32(DI), Y8            ; next second 32B
    ADDQ  $64, DI
    JMP   loop
done:
```

Key design decisions:
- **VPMADDWD for both s1 and s2**: each half's int16 values stay < 32767 (s2 max: 64×255+63×255=32,385). No VPUNPCK needed anywhere.
- **Staggered VPMADDUBSW**: s1 fires first, s2 follows — avoids all 4 competing for ports 0/5 simultaneously.
- **PREFETCHT0**: ~3% gain on Xeon cloud VMs, zero cost on Zen 4. Kept for older CPU compatibility.
- **Bottom-load with guard**: `SUBQ/JZ` before the load prevents OOB read on last iteration.
- **Merged exit reduction**: Y4×64 + Y12 combined before a single horizontal-sum pass (saves ~5 instructions vs separate reductions).

---

## 4. Go Asm Instruction Quirks (Plan 9 Dialect)

### 4.1 VPMADDUBSW operand swap

| Source | src1 role | src2 role |
|--------|-----------|-----------|
| Intel manual | **unsigned** | **signed** |
| Go Plan 9 asm | **signed** | **unsigned** |

Verified by diagnostic: `VPMADDUBSW data, ones` → data treated as signed; `VPMADDUBSW ones, data` → data treated as unsigned.

Our usage: `VPMADDUBSW Y15(ones=+1 signed), data(unsigned), dst` → correct unsigned sum.

### 4.2 VPUNPCKLWD / VPUNPCKHWD lane behavior (historical — no longer used in AVX2 path)

The SSE2 path still uses VPUNPCK for widening. In the AVX2 path, VPMADDWD has replaced VPUNPCK for both s1 and s2.

- `VPUNPCKLWD Y5(zero), Y0, Y3` — zero-extends the even-indexed 8 of 16 int16 values to 8 int32, spanning both 128-bit lanes without VEXTRACTI128.
- `VPUNPCKHWD Y5(zero), Y0, Y0` — zero-extends the odd-indexed 8 of 16 int16 values to 8 int32.

Together (8+8=16) they cover all 16 int16 results from VPMADDUBSW.

### 4.3 XMM/YMM register aliasing

`X0` is the **low 128 bits** of `Y0`, not an independent register. Writing `Y0` automatically updates `X0`. This is used in the exit reduction — no need for `VEXTRACTI128 $0, Y0, X0`.

### 4.4 VPANDN / VPTERNLOGD operand swap (MD5 core)

Go Plan 9 swaps src1/src2 for ALL non-commutative SIMD instructions:

| Instruction | Intel manual | Go Plan 9 asm |
|------------|-------------|---------------|
| `VPANDN A,B,C` | `C = ~A & B` | `C = A &^ B` (A & ~B) |
| `VPTERNLOGD imm,A,B,C` | n = (C<<2)\|\(A<<1\)\|B | n = (C<<2)\|\(B<<1\)\|A ← **swapped** |

`VPTERNLOGD` truth table immediates must be computed with Go-swapped order.
Using Intel-manual values ($0xB8/$0xCA/$0x65) produces wrong MD5 hashes.
Correct Go-swapped values: R1=$0xD8, R2=$0xAC, R4=$0x63.
See `gen_md5x8/main.go` and `gen_md5x16/main.go` for the fixed generators.

### 4.5 Go assembler limitations

- No memory operands for VPMADDUBSW (must use register src2).
- `VPBROADCASTD` is available but was the source of a bug (see §6).
- Weight tables must use `DATA /8` with little-endian uint64 encoding.

---

## 5. Evolution (optimization history)

| Version | Key change | Loop instrs | Xeon 1KB | Ryzen 64KB |
|---------|-----------|:-----------:|:--------:|:----------:|
| v0 (baseline) | Signed VPMADDUBSW + VPMOVSXWD + per-iter s1 reduction | 45 | — | — |
| — | Unsigned + VPUNPCK zero-extend | 41 | — | — |
| — | Preload lower weight table Y13 | 36 | — | — |
| — | Deferred s1 reduction | 27 | — | — |
| v1 | Bottom-load eliminates Y9/Y10 + VPBROADCASTD fix | 28 | 27.2 GB/s | 51.5 GB/s |
| v2 | VPADDW merge halves before widen (saves 6 insns) | 22 | 35.8 GB/s | 64.1 GB/s |
| v3 | PREFETCHT0 + OOB guard (safe bottom-load) | 22 | 36.6 GB/s | 59.6 GB/s |
| **v4** | **VPMADDWD pair-sum for s1** (saves 2 insns) | **20** | **42.4 GB/s** | **69.2 GB/s** |
| v5 | **VPMADDWD per-half for s2** + scalar remainder asm + merged exit reduction | **19** | 35.1 GB/s | — |
| **v6** | **CHAR_OFFSET + packing in asm** (`checksum1PackedAVX2`), combined ones table | **19** | **37.4 GB/s** | — |

**Cumulative:** 28→19 instructions (-32%), +38% throughput on Xeon (1KB). 64KB within 1% of rsync.


**VPSRLD dead-end:** Attempted packed reduction via VPSRLD (3→2 insns). Caused s1 amplification by 32768× due to garbage in upper 16 bits. Rejected — requires full 32-bit correctness for `Roll()`.

---

## 6. Bugs Fixed

### 6.1 VPBROADCASTD amplification (v0.1.x)

`VPBROADCASTD X0, Y14` replicated init_s1 into 8 lanes. Each iteration `Y4 += Y14` counted it 8×. Fixed by keeping Y14 zero-initialized (byte sums only) and applying init_s1/s2 as scalars at exit (§2.2).

### 6.2 Y15 register pollution (v0.1.3)

The s2 weight-load section used `LEAQ mul_T2<>+32(SB), AX; VMOVDQU (AX), Y15`, corrupting the all-ones table. Fixed by using separate Y13 for lower weights.

### 6.3 VPANDN operand swap — AVX2 MD5 (v0.1.4.2)

Go Plan 9 `VPANDN A,B,C` = `C = A &^ B`, NOT Intel's `C = ~A & B`.
The MD5 code generator (`gen_md5x8/main.go`) used Intel semantics for Round
1/2/4, producing wrong F functions. All 8 lanes of AVX2 MD5 were silently
wrong for every block.

**Fix**: swapped operands in generator → regenerated `md5x8_amd64.s`.
Added `TestMD5x8_AVX2_Parity` (AVX2 vs stdlib md5.Sum).

### 6.4 VPGATHERDD mask zeroing + Go asm VSIB bug (v0.1.4.2–v0.1.4.3)

Two bugs in the gather load path:
1. **Mask zeroing** — VPGATHERDD zeros the mask register after execution
   (per Intel spec). Single mask init → only first gather works.
2. **VSIB encoding** — Go Plan 9 assembler has hardcoded base register
   and broken displacement in VPGATHERDD encoding.

**Fix**: reload mask (`VPCMPEQD` / `KXNORW`) before every gather.
Use raw machine code `BYTE` opcodes to bypass Go asm VSIB bug entirely.

### 6.5 VPTERNLOGD operand swap — AVX-512 MD5 (v0.1.4.4)

Same class as §6.3: Go Plan 9 swaps src1/src2 for `VPTERNLOGD`.
Truth table index computed as `n=(dst<<2)|(src2<<1)|src1` (not Intel's
`src1`/`src2` order). All three rounds using VPTERNLOGD (R1,R2,R4) had
wrong immediates in the AVX-512 core.

Impact: 1GB identical file sync took 2+ minutes — server (Xeon w/
AVX-512) generated wrong MD5 signatures, client (stdlib MD5) found zero
matches → byte-by-byte scan.

**Fix**: recomputed all imm8 values for Go-swapped order ($0xD8/$0xAC/$0x63),
regenerated `md5x16_amd64.s`. Added `TestMD5x16_AVX512_Parity`,
`TestMD5x16_CoreOnly`, `TestMD5x16_GatherVerification`.

---

## 7. Register Map

| Register | Purpose | Lifetime |
|----------|---------|----------|
| Y15 | all-ones table (0x01 × 32) for VPMADDUBSW | constant |
| Y11 | int16 all-1s (0x0001 × 16) for VPMADDWD | constant |
| Y7 | weight table [64..33] | constant |
| Y13 | weight table [32..1] | constant |
| Y2 | current 64B block, first 32B | per-iteration |
| Y8 | current 64B block, second 32B | per-iteration |
| Y0 | temp (s1 delta via VPMADDWD) | per-iteration |
| Y3 | s2 second half | per-iteration |
| Y6 | temp (s1/s2 second half) | per-iteration |
| Y14 | running s1 (vector, byte sums only) | across iterations |
| Y4 | Σ s1_before_k (deferred s2) | across iterations |
| Y12 | Σ weighted byte sums (deferred s2) | across iterations |
| DI | data pointer | across iterations |
| SI | iteration counter | across iterations |
| R13 | init_s1 (saved for exit) | function lifetime |
| DX | init_s2 (saved for exit) | function lifetime |
| R15 | original_len (for remainder) | function lifetime |
| R12 | N = iteration count (for exit correction) | function lifetime |
| R10 | exit: s1 scalar | exit only |
| R9, R11 | exit: temp for s2 reduction | exit only |

Unused YMM registers: Y1, Y5, Y9, Y10.

---

## 8. Test Coverage

**Parity tests** (`avx2_test.go`) — compare AVX2 and SSE2 output against byte-by-byte reference (no CHAR_OFFSET):

| Test | Data | Purpose |
|------|------|---------|
| `TestAVX2Parity` (11 cases) | zeros, 0xFF, incremental, random | verify AVX2 engine |
| `TestSSE2Parity` (10 cases) | zeros, 0xFF, incremental, random | verify SSE2 engine |

**MD5 SIMD parity tests** (`md5x8_test.go`) — compare AVX2/AVX-512 MD5 against stdlib `md5.Sum`:

| Test | Scope |
|------|-------|
| `TestMD5x8_AVX2_Parity` | 8-way AVX2 MD5 vs stdlib (700-byte blocks) |
| `TestMD5x16_AVX512_Parity` | 16-way AVX-512 MD5 vs stdlib (2048-byte blocks) |
| `TestMD5x16_UnevenLengths` | 16 mixed-size blocks (63–4096 bytes) |
| `TestMD5x16_CoreOnly` | AVX-512 core with manually-built x matrix (bypasses gather) |
| `TestMD5x16_GatherVerification` | verify VPGATHERDD loads correct transposed data |

**Performance benchmark** (`tier_bench_test.go`) — three-way comparison:

| Benchmark | Engine |
|-----------|--------|
| `SSE2/*KB` | `checksum1SSE2` (32B/iter, XMM) |
| `AVX2/*KB` | `checksum1AVX2` (64B/iter, YMM) |
| `Go/*KB` | pure Go 128B batch (no SIMD) |

**Integration tests** (`delta_test.go`) — end-to-end delta round-trip, identical file, example usage.

---

## 9. Performance

**Measured on Intel Xeon Platinum (cloud VM, 2 vCPU, ~2.5 GHz):**

| Block size | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------------|:-----------:|:-----------:|:----------:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 8 KB | **42.8 GB/s** | — | 48.3 GB/s |
| 64 KB | **43.7 GB/s** | 26.7 GB/s | 44.3 GB/s |
| 1 MB | **43.6 GB/s** | 42.4 GB/s | — |

**Measured on AMD Ryzen 9 8940HX (Zen 4, laptop):**

| Block size | go-rsync v6 | v1 (baseline) | improvement |
|------------|:-----------:|:-------------:|:-----------:|
| 1 KB | 55.1 GB/s | 44.8 GB/s | +23% |
| 64 KB | **69.2 GB/s** | 51.5 GB/s | **+34%** |
| 1 MB | 51.2 GB/s | 51.2 GB/s | — |

**Cross-platform tier comparison (Ryzen 9, 64KB):**

| Tier | Throughput | vs AVX2 |
|------|:----------:|:-------:|
| AVX2 (64B/iter) | 69.2 GB/s | — |
| SSE2 (32B/iter) | 26.1 GB/s | 2.7× slower |
| Pure Go (128B batch) | 1.9 GB/s | 36× slower |

---

## 10. Appendix: SSE2 Path Notes

The SSE2 path is NOT a simple mechanical translation of AVX2. Key differences:

| Aspect | AVX2 | SSE2 | Reason |
|--------|------|------|--------|
| s1 reduction | VPMADDWD pair-sum | **VPHADDW** pair-sum | Go asm lacks XMM `VPADDW`; VPHADDW is the only XMM word-add available |
| s2 reduction | VPADDW merge + VPUNPCK | VPUNPCK per-half (no merge) | Can't use VPADDW with XMM; widen each half separately |
| Block size | 64B/iter | 32B/iter | XMM = 128-bit |
| `VPMADDWD` | YMM, int16_ones table | not used | VPMADDWD works but can't merge halves first |
| `PREFETCHT0` | 384(DI) | 384(DI) | same |
| Loop instructions | 20 | ~22 | 2 extra VPUNPCK per s2 half |

**VPBROADCASTD bug (fixed in v0.2.1):** The original SSE2 code broadcast `init_s1` into all 4 XMM lanes, causing 4× amplification. Fixed by zero-initializing X14 and applying init_s1 as scalar at exit (same approach as AVX2).

**XMM PADDW limitation:** Go Plan 9 assembler defines `APADDW` only for YMM registers (`{APADDW, ymm, Py1, ...}`). There is no XMM variant. This prevents using the VPADDW merge-before-widen optimization in SSE2, costing ~2 instructions per iteration.

---

## 11. Appendix: Per-size performance data

**Test setup:** Same Xeon Platinum cloud VM, same data pattern (`i*7%251`), both with full tail-byte handling. go-rsync via `Checksum1()` (auto-dispatch to AVX2). Measurement error ±3%.

| Size | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------|:---:|:---:|:---:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 4 KB | — | 36.8 GB/s | 48.3 GB/s |
| 16 KB | — | 39.2 GB/s | 49.0 GB/s |
| 64 KB | **43.7 GB/s** | 40.7 GB/s | 44.3 GB/s |
| 97 KB | — | 41.1 GB/s | 44.8 GB/s |
| 128 KB | — | 41.3 GB/s | 45.1 GB/s |
| 256 KB | — | 41.5 GB/s | 45.2 GB/s |

> v6 closed the 1KB gap from -62% (v4) to -14% (v6). 64KB now within 1.4% of rsync. Remaining 1KB gap is due to rsync's VPSRLD/VPSRLDQ + exit-correction approach, which trades code clarity for ~15% more port throughput on Xeon.