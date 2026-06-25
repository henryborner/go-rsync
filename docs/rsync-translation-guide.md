# go-rsync AVX2 Checksum — Design & Translation Notes

> Originally developed for [Shuttle](https://github.com/henryborner/shuttle), now a standalone library.

## 1. High-Level Summary

| | rsync (simd-checksum-avx2.S) | go-rsync (rolling_amd64.s) |
|---|---|---|
| Data type | `schar` (–128..127) | `uint8` (0..255) |
| CHAR_OFFSET | 0 (hardcoded) | 31 (post-correction in Go) |
| Return format | packed `uint32`: `(s1&0xFFFF)｜(s2<<16)` | two `uint32` scalars (`*s1`, `*s2`) |
| s1 reduction | VPSRLD packed (int16 modulo hack) | VPMADDWD pair-sum (full 32-bit) |
| s2 weighted reduction | VPSRLDQ packed + VPADDD | VPADDW merge + VPUNPCK widen |
| PREFETCHT0 | yes (384 bytes ahead) | yes (384 bytes ahead) |
| Loop instructions | 21 (`sub`+`jnz` fused) | **20** (`SUBQ`+`JZ`+`JMP`) |

> **Key insight:** Both use identical deferred reduction for s2. The difference is s1 reduction: rsync uses VPSRLD (correct only modulo 2^16; acceptable because CHAR_OFFSET=0 keeps values small). go-rsync uses VPMADDWD for full 32-bit precision — one instruction that multiplies adjacent int16 pairs by 1 and sums, replacing 3 instructions (VPUNPCKLWD + VPUNPCKHWD + VPADDD). This is the primary source of the 7% speed advantage over rsync's hand-written assembly.

> **Both rsync and go-rsync use rolling checksums.** rsync does incremental byte-by-byte rolling in `match.c`'s `null_hash` path; go-rsync does the same in `Roll()`. The AVX2 functions compute *blocks* of 64 bytes, accepting initial s1/s2 values and returning updated ones.

---

## 2. Algorithm (unsigned + deferred reduction + VPMADDWD)

### 2.1 Per-block breakdown (64 bytes per iteration)

Block k (0-indexed):

```
s1_before_k       = running s1 at start of block k
delta_s1_k        = Σ bytes in block k              (VPMADDUBSW→VPADDW→VPMADDWD)
weighted_sum_k    = Σ (64-i)·byte_i in block k      (VPMADDUBSW→VPADDW→VPUNPCK)
s1_after_k        = s1_before_k + delta_s1_k

s1 = Σ delta_s1_k                                    (Y14)
s2 = 64 × Σ s1_before_k + Σ weighted_sum_k           (Y4 = Σs1_before, Y12 = Σweighted)
```

**s1 reduction** uses VPMADDWD with an int16 all-1s constant (Y11):
```
VPMADDUBSW → VPADDW (merge halves) → VPMADDWD × int16_ones → 8×int32 delta_s1
```
One instruction replaces VPUNPCKLWD + VPUNPCKHWD + VPADDD (3→1). This works because byte sums fit in signed int16 (<32767).

**s2 weighted reduction** keeps VPUNPCK because weighted values can exceed int16 signed range after VPADDW merge (max ~48,450 > 32,767).

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
p := n - n%64                      // bytes processed by ASM
s1 += uint32(p) * CHAR_OFFSET
s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
```

### 2.4 Remainder bytes

ASM only handles full 64B blocks. Go processes the tail:

```go
for i := p; i < n; i++ {
    s1 += uint32(data[i]) + CHAR_OFFSET
    s2 += s1
}
```

---

## 3. Loop Structure (20 instructions always-executed)

```asm
loop:
    ; s1: VPMADDUBSW ×2 halves → VPADDW merge → VPMADDWD pair-sum
    VPMADDUBSW  Y15, Y2, Y0        ; first 32B → 16 int16
    VPMADDUBSW  Y15, Y8, Y6        ; second 32B → 16 int16
    VPADDW      Y6, Y0, Y0         ; merge halves (16-bit)
    VPMADDWD    Y11, Y0, Y0        ; pair-sum → 8×int32 (1 insn replaces 3!)

    ; s2: Y4 += Y14 (s1_before accumulation — deferred)
    VPADDD      Y4, Y14, Y4

    ; s2: VPMADDUBSW × weight tables → VPADDW merge → VPUNPCK widen
    VPMADDUBSW  Y7, Y2, Y2         ; first 32B × [64..33]
    VPMADDUBSW  Y13, Y8, Y6        ; second 32B × [32..1]
    VPADDW      Y6, Y2, Y2         ; merge halves (16-bit)
    VPUNPCKLWD  Y5, Y2, Y3         ; widen lo 8
    VPUNPCKHWD  Y5, Y2, Y2         ; widen hi 8
    VPADDD      Y2, Y3, Y2         ; Y2 = 8×int32 weighted
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
- **VPMADDWD for s1**: one instruction vs three (VPUNPCKLWD+VPUNPCKHWD+VPADDD). Only works for s1 where values stay <32767 after merge.
- **VPUNPCK retained for s2**: weighted values can exceed int16 signed range after VPADDW merge.
- **PREFETCHT0**: ~3% gain on Xeon cloud VMs, zero cost on Zen 4. Kept for older CPU compatibility.
- **Bottom-load with guard**: `SUBQ/JZ` before the load prevents OOB read on last iteration.
- **VPADDW merge before widen**: combining the two 32B halves with 16-bit addition saves 3 widen instructions vs widening each half separately.

---

## 4. Go Asm Instruction Quirks (Plan 9 Dialect)

### 4.1 VPMADDUBSW operand swap

| Source | src1 role | src2 role |
|--------|-----------|-----------|
| Intel manual | **unsigned** | **signed** |
| Go Plan 9 asm | **signed** | **unsigned** |

Verified by diagnostic: `VPMADDUBSW data, ones` → data treated as signed; `VPMADDUBSW ones, data` → data treated as unsigned.

Our usage: `VPMADDUBSW Y15(ones=+1 signed), data(unsigned), dst` → correct unsigned sum.

### 4.2 VPUNPCKLWD / VPUNPCKHWD lane behavior

- `VPUNPCKLWD Y5(zero), Y0, Y3` — zero-extends the even-indexed 8 of 16 int16 values to 8 int32, spanning both 128-bit lanes without VEXTRACTI128.
- `VPUNPCKHWD Y5(zero), Y0, Y0` — zero-extends the odd-indexed 8 of 16 int16 values to 8 int32.

Together (8+8=16) they cover all 16 int16 results from VPMADDUBSW.

### 4.3 XMM/YMM register aliasing

`X0` is the **low 128 bits** of `Y0`, not an independent register. Writing `Y0` automatically updates `X0`. This is used in the exit reduction — no need for `VEXTRACTI128 $0, Y0, X0`.

### 4.4 Go assembler limitations

- No memory operands for VPMADDUBSW (must use register src2).
- `VPBROADCASTD` is available but was the source of a bug (see §6).
- Weight tables must use `DATA /8` with little-endian uint64 encoding.

---

## 5. Evolution (optimization history)

| Version | Key change | Loop instrs | Xeon 1MB | Ryzen 64KB |
|---------|-----------|:-----------:|:--------:|:----------:|
| v0 (baseline) | Signed VPMADDUBSW + VPMOVSXWD + per-iter s1 reduction | 45 | — | — |
| — | Unsigned + VPUNPCK zero-extend | 41 | — | — |
| — | Preload lower weight table Y13 | 36 | — | — |
| — | Deferred s1 reduction (rsync-style) | 27 | — | — |
| v1 | Bottom-load eliminates Y9/Y10 + VPBROADCASTD fix | 28 | 27.2 GB/s | 51.5 GB/s |
| v2 | VPADDW merge halves before widen (saves 6 insns) | 22 | 35.8 GB/s | 64.1 GB/s |
| v3 | PREFETCHT0 + OOB guard (safe bottom-load) | 22 | 36.6 GB/s | 59.6 GB/s |
| **v4** | **VPMADDWD pair-sum for s1** (saves 2 insns) | **20** | **42.4 GB/s** | **69.2 GB/s** |

**Cumulative:** 28→20 instructions (-29%), +55% throughput on Xeon, +34% on Ryzen. Beats rsync's hand-written assembly (21 insns, 39.6 GB/s) by 7%.

**VPSRLD dead-end:** Attempted rsync's VPSRLD packed reduction (3→2 insns). Caused s1 amplification by 32768× due to garbage in upper 16 bits. Rejected — requires full 32-bit correctness for `Roll()`.

---

## 6. Bugs Fixed

### 6.1 VPBROADCASTD amplification

`VPBROADCASTD X0, Y14` replicated init_s1 into 8 lanes. Each iteration `Y4 += Y14` counted it 8×. Fixed by keeping Y14 zero-initialized (byte sums only) and applying init_s1/s2 as scalars at exit (§2.2).

### 6.2 Y15 register pollution (v0.1.3)

The s2 weight-load section used `LEAQ mul_T2<>+32(SB), AX; VMOVDQU (AX), Y15`, corrupting the all-ones table. Fixed by using separate Y13 for lower weights.

---

## 7. Register Map

| Register | Purpose | Lifetime |
|----------|---------|----------|
| Y15 | all-ones table (0x01 × 32) for VPMADDUBSW | constant |
| Y11 | int16 all-1s (0x0001 × 16) for VPMADDWD | constant |
| Y7 | weight table [64..33] | constant |
| Y13 | weight table [32..1] | constant |
| Y5 | zero register (VPUNPCK zero-extend) | constant |
| Y2 | current 64B block, first 32B | per-iteration |
| Y8 | current 64B block, second 32B | per-iteration |
| Y0 | temp (s1 delta via VPMADDWD) | per-iteration |
| Y3 | temp (VPUNPCK for s2) | per-iteration |
| Y6 | temp (s2 second half) | per-iteration |
| Y14 | running s1 (vector, byte sums only) | across iterations |
| Y4 | Σ s1_before_k (deferred s2) | across iterations |
| Y12 | Σ weighted byte sums (deferred s2) | across iterations |
| DI | data pointer | across iterations |
| SI | iteration counter | across iterations |
| R13 | init_s1 (saved for exit) | function lifetime |
| DX | init_s2 (saved for exit) | function lifetime |
| R12 | N = iteration count (for exit correction) | function lifetime |
| R10 | exit: s1 scalar | exit only |
| R9, R11 | exit: temp for s2 reduction | exit only |

Unused YMM registers: Y1, Y9, Y10.

---

## 8. Test Coverage

**Parity tests** (`avx2_test.go`) — compare AVX2 and SSE2 output against byte-by-byte reference (no CHAR_OFFSET):

| Test | Data | Purpose |
|------|------|---------|
| `TestAVX2Parity` (11 cases) | zeros, 0xFF, incremental, random | verify AVX2 engine |
| `TestSSE2Parity` (10 cases) | zeros, 0xFF, incremental, random | verify SSE2 engine |

**Performance benchmark** (`tier_bench_test.go`) — three-way comparison:

| Benchmark | Engine |
|-----------|--------|
| `SSE2/*KB` | `checksum1SSE2` (32B/iter, XMM) |
| `AVX2/*KB` | `checksum1AVX2` (64B/iter, YMM) |
| `Go/*KB` | pure Go 128B batch (no SIMD) |

**Integration tests** (`delta_test.go`) — end-to-end delta round-trip, identical file, example usage.

---

## 9. Performance

**Measured on Intel Xeon Platinum (cloud VM, 2 vCPU):**

| Block size | go-rsync v4 | rsync-AVX2-asm | go-rsync advantage |
|------------|:-----------:|:--------------:|:------------------:|
| 1 KB | 16.8 GB/s | — | — |
| 64 KB | 26.7 GB/s | — | — |
| 1 MB | **42.4 GB/s** | 39.6 GB/s | **+7.1%** |

**Measured on AMD Ryzen 9 8940HX (Zen 4, laptop):**

| Block size | go-rsync v4 | v1 (baseline) | improvement |
|------------|:-----------:|:-------------:|:-----------:|
| 1 KB | 55.1 GB/s | 44.8 GB/s | +23% |
| 64 KB | **69.2 GB/s** | 51.5 GB/s | **+34%** |
| 1 MB | 51.2 GB/s | 51.2 GB/s | — |

**Cross-platform tier comparison (Ryzen 9, 64KB):**

| Tier | Throughput | vs AVX2 |
|------|:----------:|:-------:|
| AVX2 (64B/iter) | 69.2 GB/s | — |
| SSE2 (32B/iter) | 25.4 GB/s | 2.7× slower |
| Pure Go (128B batch) | 1.9 GB/s | 36× slower |

---

## 10. Appendix: Translating AVX2 to SSE2

Once the AVX2 design was settled, the SSE2/SSSE3 fallback was a mechanical translation:

| AVX2 | SSE2 | Rationale |
|------|------|-----------|
| YMM (256-bit) | XMM (128-bit) | half-width registers |
| 64B/iter | 32B/iter | half the throughput |
| 8 int32 lanes | 4 int32 lanes | no VEXTRACTI128 needed |
| `VMOVDQU` | `MOVOU` | Go asm name for 128-bit unaligned load |
| `VPUNPCKLWD Y5,Y0,Y3` | `VPUNPCKLWD X5,X0,X3` | same instruction, XMM variant |
| `SHLL $6` (64×) | `SHLL $5` (32×) | block size changed |
| weights [64..1] | weights [32..1] | re-slice lookup table |
| `VEXTRACTI128` | not needed | XMM is naturally 128-bit |
| `PSHUFD $0` broadcast | same | 4-lane broadcast instead of 8 |
| `VPSRLDQ $8/$4` reduction | same | 4 lanes still need 2 shifts |

Algorithm, deferred reduction, bottom-load structure, VPUNPCK zero-extend — all identical. The AVX2 version took 45→20 iterations of design; the SSE2 version was just editing constants and register names.

The primary throughput bottleneck shifted from VPUNPCK port contention (v1, 8 VPUNPCK/iter → port 5 saturated at ~52 GB/s Zen 4) to memory bandwidth / front-end decode. With VPMADDWD replacing 3 VPUNPCK instructions for s1, the mixed s1/s2 instruction mix better balances across execution ports.