# go-rsync MD5 AVX2 Acceleration Notes

> **For future maintainers and our future selves.**

## Overview

go-rsync is the first Go rsync/delta library with AVX2-accelerated MD5. The
fast path achieves **~2.35 GB/s** single-threaded on an AMD Ryzen 9 8940HX
(Windows, Go 1.x), processing 1MB of data into ~1500 MD5 block signatures in
~447µs with only 3 heap allocations.

## Architecture

```
GenerateSignature(data, blockSize, "md5")
│
├─ Checksum1 (rolling weak checksum) ───────────────────────┐
│   ├─ [amd64] checksum1AVX2  → 64B/iter SIMD               │  files:
│   │   rolling_amd64.s                                     │  rolling_fast_amd64.go
│   │   rolling_avx2_decl.go                                 │  rolling_amd64.s
│   ├─ [amd64] checksum1SSE2  → 32B/iter fallback           │  rolling_sse2_amd64.s
│   │   rolling_sse2_amd64.s                                 │
│   └─ [!amd64] byte-by-byte loop                           │  rolling_generic.go
│       rolling_generic.go                                   │
│                                                            │
└─ md5Hash8wayAVX2 (strong checksum) ────────────────────────┤
    ├─ Phase 1: 8 blocks × N full 64B chunks                │  files:
    │   for each chunk:                                      │  md5x8_amd64.go
    │     md5x8LoadTranspose(data, &offsets, chunk, &x)      │  md5x8_load_transpose_amd64.s
    │     md5x8core(&x, &state)                              │  md5x8_amd64.s
    │                                                        │  md5x8_common.go
    ├─ Phase 2: tail + padding                               │  md5x8_transpose.s (tail only)
    │   md5Finalize8way  → identical tails, batched SIMD     │
    │   md5FinalizeScalar → per-lane fallback                │
    └─ [!amd64] scalar md5.Sum (stdlib)                      │  md5x8_generic.go (stub)
```

## Key Files

| File | Purpose |
|------|---------|
| `md5x8_amd64.s` | 64 unrolled AVX2 MD5 steps across 8 lanes (YMM regs). **Generated** by `gen_md5x8/main.go`. |
| `md5x8_transpose.s` | Hand-written: 8×64 contiguous → 16 transposed YMMs. Used by tail-finalization only. |
| `md5x8_load_transpose_amd64.s` | Loads 8 scattered 64B blocks directly from source data and transposes into 16 YMMs in one pass. **Eliminates the intermediate 512B copy.** |
| `md5x8_amd64.go` | Go-side glue: `md5Hash8wayAVX2`, `md5Finalize8way`, `md5FinalizeScalar`. |
| `md5x8_common.go` | Shared constants (`t256`, `shifts`) and `md5FinalLane` (pure-Go single-lane finalization). |
| `md5x8_generic.go` | Stubs for non-amd64: `md5x8available()→false`, `md5Hash8wayAVX2→panic`. |
| `md5x8_test.go` | Pure-Go 8-way reference, validation tests, benchmarks. |
| `gen_md5x8/main.go` | Code generator for `md5x8_amd64.s`. Run with `go run .` from its directory. |
| `rolling_fast_amd64.go` | `Checksum1` with inlined AVX2 path + 4-byte remainder. Also contains `checksum1` fallback (AVX2→SSE2→Go). |
| `rolling_amd64.s` | AVX2 checksum asm: 64B/iter, VPMADDUBSW + VPMADDWD + deferred reduction. |
| `rolling_sse2_amd64.s` | SSE2/SSSE3 checksum: 32B/iter, XMM registers. |
| `rolling_avx2_decl.go` | `//go:noescape` decl for `checksum1AVX2`. |
| `rolling_sse2_decl.go` | `//go:noescape` decl for `checksum1SSE2`. Critical for zero-alloc. |
| `rolling_generic.go` | `!amd64` fallback: byte-by-byte checksum + `Checksum1`. |
| `rolling.go` | `RollingSum` type, `Reset/Roll/Value`. No `Checksum1` here — it's split by build tag. |
| `registry.go` | Pluggable hash algorithm registry with `FastSum` optional field. |
| `registry_stdlib.go` | Registers md5/sha256/xxh64 with `FastSum` implementations. |

## Critical Design Decisions

### 1. Why VPINSRD + VINSERTI128 instead of VPGATHERDD?

`VPGATHERDD` is the "obvious" way to load 8 scattered uint32s into a YMM in one
instruction. It does not work on AMD Zen 4 (Ryzen 8940HX) — the instruction
executes but returns zeros. This was verified with 20+ encoding variants,
objdump inspection, and Go source analysis. Intel CPUs likely work, but we
cannot rely on it. **Do not attempt to reintroduce VPGATHERDD.**

Instead, we use the VPINSRD shuffle pattern:
```
MOVL (ptr0), tmp; VMOVD tmp, X0        // lane 0
MOVL (ptr1), tmp; VPINSRD $1, tmp, X0  // lane 1
...
VINSERTI128 $1, X1, Y0, Y0             // merge halves
```
This is slower than VPGATHERDD would be, but it actually works.

### 2. `//go:noescape` is mandatory for zero-alloc

Both `checksum1AVX2` and `checksum1SSE2` take `*uint32` pointers. Without
`//go:noescape`, Go's escape analysis moves the local `s1, s2` variables to the
heap — adding 1498 allocations per `GenerateSignature` call. The annotation
promises to the compiler that the asm does not store those pointers. The asm
only reads and writes through them (MOVL (CX), R13 ... MOVL R10, (CX)).

### 3. The intermediate buf copy was a bottleneck

The original design had Go code copy 8×64 bytes from scattered source positions
into a contiguous `[8][64]byte` buffer, then called `md5x8transpose` to build
the transposed message words. This copy + transpose accounted for ~17% of total
time. `md5x8LoadTranspose` eliminates the copy by loading directly from 8
scattered addresses into transposed YMMs in one asm step.

### 4. 4-byte remainder in Checksum1

The AVX2 checksum processes multiples of 64 bytes. The remaining 1–63 bytes
were originally processed one byte at a time in a Go loop — 90,000 iterations
for a 1MB file (60 remainder bytes × 1500 blocks). The 4-byte-at-a-time
formula reduces this to ~22,500 iterations, saving ~110µs.

### 5. Build tags split

- `rolling.go` — no tag, common types (`RollingSum`, `CHAR_OFFSET`)
- `rolling_fast_amd64.go` — `//go:build amd64` — fast `Checksum1` + `checksum1`
- `rolling_generic.go` — `//go:build !amd64` — generic `Checksum1` + `checksum1`
- `md5x8_amd64.go` — `//go:build amd64` — all AVX2-specific MD5 code
- `md5x8_generic.go` — `//go:build !amd64` — stubs

This avoids "undefined symbol" errors on macOS ARM64 (`darwin,arm64`).

## Performance (AMD Ryzen 9 8940HX, 1MB data, blockSize≈700)

| Benchmark | Time | Allocs | Throughput |
|-----------|------|--------|------------|
| GenerateSignature (md5) | ~447µs | 3 | 2.35 GB/s |
| GenerateSignature (sha256) | ~819µs | — | 1.28 GB/s |
| GenerateSignature (xxh64) | ~318µs | — | 3.30 GB/s |
| GenerateSignatureParallel (md5) | ~245µs | — | 4.27 GB/s |

## Regenerating md5x8_amd64.s

```bash
cd gen_md5x8
go run .
```

This overwrites `../md5x8_amd64.s`. The generated file includes md5x8core and
DATA tables. **DO NOT** edit the generated file by hand — fix the generator
instead.

## Safety Checklist When Modifying

- [ ] Run `go vet .` — must produce zero warnings
- [ ] Run `go test -count=1 .` — all 16 tests must pass
- [ ] Run `go test -bench='^BenchmarkSignature$' -benchtime=1s -count=3 .`
- [ ] Check `VZEROUPPER` before every `RET` in functions that touch YMM regs
- [ ] Check `//go:noescape` on all asm decls that take pointer args
- [ ] Check that `$0-N` frame size matches actual args (slice=24 bytes!)
- [ ] Verify non-amd64 builds: `GOOS=darwin GOARCH=arm64 go build .`

## Known Quirks

- **Go Plan 9 operand swap**: `VPMADDUBSW(signed, unsigned)` in Go asm vs.
  `VPMADDUBSW(unsigned, signed)` in Intel manual. Our operands are correct:
  `VPMADDUBSW Y_ones(signed +1), Y_data(unsigned), Y_dst`.
- **Slice args in asm**: `data []byte` occupies 24 bytes in the frame
  (ptr+len+cap), not 16. Getting this wrong causes SIGSEGV at small addresses.
- **AMD Zen 4 VPGATHERDD**: Broken on this microarchitecture. Do not use.
- **PowerShell `Set-Content`**: Changes file encoding from UTF-8 to UTF-16 on
  Windows. If you edit `.s` files with PowerShell, verify encoding afterward.
