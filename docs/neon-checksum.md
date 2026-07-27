# ARM64 NEON Rolling Checksum

> Two-tier NEON acceleration: UDOT (ARMv8.2+dotprod, 27 GB/s) + VUMULL (baseline, 12 GB/s).
> CPU feature detection with automatic fallback. GNU-as-verified WORD encodings.
> QEMU + ARM64 CI + Ampere Altra tested. Comprehensive correctness suite.

## Overview

| Feature | Value |
|---------|-------|
| Architecture | ARM64 NEON (128-bit SIMD) |
| Current version | **v0.4.0** — tiered UDOT / VUMULL dispatch |
| UDOT path | 4 insns/64B, **27 GB/s** (requires dotprod) |
| VUMULL path | 20 insns/64B, **12 GB/s** (all ARM64) |
| Block size | 2×32B per iteration (64B unrolled) |
| CHAR_OFFSET | Post-correction in Go layer (`p = n - n%64`) |
| Loop control | CBNZ (NEON clobbers NZCV flags) |
| Feature detection | `golang.org/x/sys/cpu` ARM64.HasASIMDDP |

## Performance (CI: ubuntu-24.04-arm)

| Size | Pure Go | VUMULL | UDOT | UDOT vs Go |
|------|--------|--------|------|:--:|
| 8 KB | 3,382 MB/s | 11,909 MB/s | 26,073 MB/s | 7.7x |
| 64 KB | 3,387 MB/s | 12,036 MB/s | 26,927 MB/s | 8.0x |
| 1024 KB | 3,391 MB/s | 11,958 MB/s | 25,820 MB/s | 7.6x |

Comparison across GitHub runners:

| Platform | CPU | SIMD | 1024KB |
|----------|-----|------|--------|
| AMD64 | EPYC 7763 | AVX2 VPMADDUBSW | 49,435 MB/s |
| ARM64 | — | UDOT (dotprod) | 25,820 MB/s |
| ARM64 | — | VUMULL (baseline) | 11,958 MB/s |

## Architecture

### UDOT path (v12, primary)

```
UDOT V12.4S, Vn.16B, Vm.16B
→ V12.S[i] += Σ(j=0..3) byte[4i+j] × weight[4i+j]

4 UDOT per 64B — replaces entire 20-instruction VUMULL chain.
Weight table: byte-packed (same layout as VUMULL).
```

### VUMULL path (v10, fallback)

```
VUMULL  ×8 — byte×weight → halfword
VADDP   ×4 — pairwise reduce
VSADDLP ×4 — pair-sum to int32
VADD    ×4 — merge halves
```

Fallback for pre-2017 ARM64 CPUs without dotprod.

### Dispatch

```go
if cpu.ARM64.HasASIMDDP && n >= 64 {
    checksum1NEON_dotprod(data, &s1, &s2)  // UDOT, 27 GB/s
} else if n >= 64 {
    checksum1NEON(data, &s1, &s2)          // VUMULL, 12 GB/s
} else {
    // pure Go 128B batched fallback
}
```

## Version History

| Ver | Approach | 1024KB MB/s | Notes |
|-----|----------|:--:|------|
| v1–v8 | VUMULL/VMLAL variants | — | All had s2 correctness bugs (undetected) |
| v9 | VMLAL, fixed weights | 8,930 | Correct but slow (16 serial VMLAL) |
| **v10** | **VUMULL, correct order** | **11,958** | **Verified correct. Fallback path.** |
| v11 | VUMULL 128B unroll | 11,926 | No gain — memory bound |
| **v12** | **UDOT dotprod** | **25,820** | **4 insns/64B. Primary path.** |
| v13 | UDOT 128B unroll | 23,794 | Regression — I-cache pressure |

> ⚠️ v1–v8 s2 was WRONG. Passed CI because `TestChecksum1Parity` compared NEON vs NEON.
> v9+ fixed with `TestNEONParityRaw` cross-validation against pure-Go reference.

## WORD Encoding Reference

### UDOT (dotprod path)

| Instruction | WORD |
|-------------|------|
| UDOT V12.4S, V2.16B, V18.16B | `$0x6E92944C` |
| UDOT V12.4S, V3.16B, V19.16B | `$0x6E93946C` |
| UDOT V12.4S, V20.16B, V18.16B | `$0x6E92968C` |
| UDOT V12.4S, V21.16B, V19.16B | `$0x6E9396AC` |

Generated with: `aarch64-linux-gnu-as -march=armv8.2-a+dotprod`

### VUMULL (fallback path)

| Instruction | WORD |
|-------------|------|
| VUMULL V5.8H, V2.8B, V18.8B | `$0x2E32C045` |
| VUMULL2 V6.8H, V2.16B, V18.16B | `$0x6E32C046` |
| VUMULL V7.8H, V3.8B, V19.8B | `$0x2E33C067` |
| VUMULL2 V8.8H, V3.16B, V19.16B | `$0x6E33C068` |
| VUMULL V24.8H, V20.8B, V18.8B | `$0x2E32C298` |
| VUMULL2 V25.8H, V20.16B, V18.16B | `$0x6E32C299` |
| VUMULL V26.8H, V21.8B, V19.8B | `$0x2E33C2BA` |
| VUMULL2 V27.8H, V21.16B, V19.16B | `$0x6E33C2BB` |
| VSADDLP V5.4S, V5.H8 | `$0x4E6028A5` |
| VSADDLP V7.4S, V7.H8 | `$0x4E6028E7` |
| VSADDLP V24.4S, V24.H8 | `$0x4E602B18` |
| VSADDLP V26.4S, V26.H8 | `$0x4E602B5A` |

### Weight table (byte-packed, shared by both paths)

```
DATA w32_neon<>+0(SB)/8,  $0x191a1b1c1d1e1f20   // weights 32,31,...,25
DATA w32_neon<>+8(SB)/8,  $0x1112131415161718   // weights 24,23,...,17
DATA w32_neon<>+16(SB)/8, $0x090a0b0c0d0e0f10   // weights 16,15,...,9
DATA w32_neon<>+24(SB)/8, $0x0102030405060708   // weights 8,7,...,1
```

## Key Discoveries

### Go 1.26 ARM64 SIMD operand order (critical)

Go ARM64 assembler uses destination-LAST but VADD and VADDP map first-two operands
differently:

| Instruction | Go syntax | Encoding |
|-------------|-----------|----------|
| VADD | `VADD Rn.T, Rm.T, Rd.T` | Rd = Rn + Rm |
| VADDP | `VADDP Rm.T, Rn.T, Rd.T` | Rd = pair(Rn, Rm) |

VADDP and VADD swap Rn/Rm mapping — never assume, always verify with objdump.

### VMLAL requires halfword weights (v7 bug)

VMLAL reads weight registers as `.4H`/`.8H` (halfwords), but the weight table
was byte-packed for VUMULL. Each pair of consecutive bytes was misinterpreted
as a single halfword, producing garbage weights.

### TestChecksum1Parity is self-consistency, not correctness

CI's existing test compared NEON vs NEON — both compute the same (potentially wrong)
result. v9+ added `TestNEONParityRaw` that validates against a pure-Go reference.

## Lessons Learned

1. **Architecture-specific instructions matter** — UDOT (ARMv8.2 dotprod) is 2x faster than
   VUMULL. Intel's VPMADDUBSW is another 2x on top. ISA beats generic SIMD every time.

2. **Go 1.26 ARM64 SIMD operand order is inconsistent** — VADD and VADDP differ.
   Always verify with objdump or use WORD encodings.

3. **Memory bandwidth is the limiting factor** — checksum is 2-3 ops per byte loaded.
   128B unrolling and other compute optimizations showed zero gain on CI VM.

4. **CBNZ, not BEQ** — NEON destroys ARM condition flags.

5. **Don't hand-compute WORD encodings** — use GNU as + objdump.

6. **Cross-validation tests are essential** — without `TestNEONParityRaw`, v1-v8's s2 bug
   would have shipped undetected.

## Files

| File | Role |
|------|------|
| `rolling_neon_dotprod_arm64.s` | UDOT path (primary, 4 insns/64B) |
| `rolling_neon_arm64.s` | VUMULL fallback (20 insns/64B) |
| `rolling_fast_arm64.go` | CPU feature detection + dispatch |
| `align_test.go` | 80+ correctness tests |
| `docs/neon-checksum.md` | This document |

## Future

- Go 1.27 native mnemonics for VUMULL/UDOT/VMLAL (issue #78498)
- SVE/SVE2 on Graviton3+ / Neoverse V1+
- Apple M-series AMX coprocessor

