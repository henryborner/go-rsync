# go-rsync

[![Go Reference](https://pkg.go.dev/badge/github.com/henryborner/go-rsync.svg)](https://pkg.go.dev/github.com/henryborner/go-rsync)
[![Go](https://img.shields.io/badge/Go-1.21+-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**Go implementation of the rsync delta-transfer algorithm** — with AVX2/AVX-512 accelerated MD5 (8-way + 16-way SIMD). Rolling checksum matching, block signature generation, file reconstruction, and a binary wire protocol.

Built to power [Shuttle](https://github.com/henryborner/shuttle), my own Windows-native file sync tool — this library was extracted from Shuttle and is its core delta-transfer engine.

## Features

- **8-way AVX2 MD5** — 8 blocks hashed in parallel via AVX2 assembly (YMM), VPGATHERDD gather load + transpose.
- **16-way AVX-512 MD5** — 16 blocks in parallel (ZMM), blockSize ≥ 2KB.
- **8-way AVX2 SHA-256** — same SIMD approach as MD5, for integrity-critical workloads.
- **3-tier checksum engine** — AVX2 (64B/iter) → SSE2 (32B/iter) → pure Go 128B batch. Auto-detects CPU at runtime.
- **Pluggable strong hash** — md5, sha256, xxh64, xxh3-128 built-in. Register your own with `FastSum` support.
- **Binary wire protocol** — compact big-endian encoding, ready for SSH pipes.
- **Streaming I/O** — `GenerateSignatureReader`, `SearchReader`, stream decode — O(blockSize) memory for multi-GB files.
- **Parallel APIs** — `GenerateSignatureParallel`, `SearchParallel` (5-7× on 8 cores).
- **Rolling checksum** — CHAR_OFFSET=31, uint32 natural-overflow arithmetic.
- **Well tested** — roundtrip, fuzz, parity (AVX2 vs SSE2 vs pure Go), MD5 8-way + 16-way (AVX2 + AVX-512 vs stdlib).

## 📦 Install

```bash
go get github.com/henryborner/go-rsync
```text

## 🚀 Quick start

```go
package main

import (
    "os"
    delta "github.com/henryborner/go-rsync"
)

func main() {
    oldFile, _ := os.ReadFile("v1.bin")
    newFile, _ := os.ReadFile("v2.bin")

    blockSize := delta.CalculateBlockSize(int64(len(oldFile)))

    // One-liner: compute delta and reconstruct
    result, err := delta.RoundTrip(oldFile, newFile, blockSize, "md5")
    if err != nil {
        panic(err)
    }
    os.WriteFile("v2_reconstructed.bin", result, 0644)
}
```text

For network use, split into sender/receiver:

```go
// --- Sender side ---
insts := delta.Delta(oldFile, newFile, blockSize, "md5")
delta.WireEncodeInstructions(conn, insts)

// --- Receiver side ---
delta.ApplyDeltaStream(oldFile, conn, outputFile, blockSize, "md5")
```text

## 📊 Benchmarks

**AMD Ryzen 9 8940HX (Zen 4), single-threaded, 1MB data, blockSize≈700:**

| Benchmark | Time | Throughput |
| ----------- | ------ | ------------ |
| `GenerateSignature` (md5) | ~345 µs | **2.90 GB/s** |
| `GenerateSignature` (xxh64) | ~152 µs | 6.57 GB/s |
| `GenerateSignature` (xxh3) | ~231 µs | 4.33 GB/s |
| `GenerateSignature` (sha256) | ~617 µs | 1.62 GB/s |
| `GenerateSignatureParallel` (100MB, 32-thread) | ~2.63 ms | **39.9 GB/s** |

**Intel Xeon Platinum @ 2.5GHz, AVX-512 enabled:**

| Benchmark | Time | Throughput |
| ----------- | ------ | ------------ |
| `MD5x8_Bulk` (AVX2 8-way, 32KB) | 11.5 µs | **2.84 GB/s** |
| `MD5x8Core_Bulk` (AVX2 raw, 1000×64B×8) | 145 µs | 3.54 GB/s |
| `MD5x16Core_Bulk` (AVX-512 raw, 1000×64B×16) | 94 µs | **10.86 GB/s** |
| `SignatureReader/10MB_32KB` | 2.88 ms | 3.64 GB/s |

**Checksum1 (rolling weak checksum) throughput:**

| Data size | AVX2 (Ryzen) | AVX2 (Xeon) | rsync-AVX2 (Xeon) |
| ----------- | :---: | :---: | :---: |
| 1 KB | **63 GB/s** | 37 GB/s | 43 GB/s |
| 64 KB | **77 GB/s** | 44 GB/s | 44 GB/s |
| 1 MB | **77 GB/s** | 44 GB/s | — |

> 64KB within 1.4% of rsync on Xeon. AVX-512 raw MD5 core hits 10.9 GB/s.

Run on your own machine:

```bash
go test -bench='BenchmarkSignature$|BenchmarkMD5x8_Bulk|BenchmarkChecksum1' -benchmem .
```text

## 📁 Package layout

| File | Purpose |
| ------ | --------- |
| `match.go` | Block matching engine, signature generation |
| `reconstruct.go` | File reconstruction from instruction stream |
| `wire.go` | Binary wire protocol encode/decode |
| `registry.go` | Pluggable strong-hash registry |
| `api.go` | High-level convenience API: `Delta`, `ApplyDelta`, `RoundTrip`, `ApplyDeltaStream` |
| `rolling.go` | Rolling checksum (`RollingSum`, `Checksum1`) |
| `rolling_amd64.s` | AVX2 checksum assembly (64B/iter) + `checksum1PackedAVX2` |
| `rolling_sse2_amd64.s` | SSE2/SSSE3 checksum assembly (32B/iter) |
| `rolling_fast_amd64.go` | Tiered dispatch: AVX2 → SSE2 → Go, inlined `Checksum1` |
| `rolling_generic.go` | Portable byte-by-byte checksum (non-amd64 fallback) |
| `md5x8_amd64.s` | **Generated** — 64-step unrolled AVX2 MD5 core (8-way) |
| `md5x8_transpose_fast_amd64.s` | Register-shuffle transpose (~80 vs ~320 VPINSRD instructions) |
| `md5x8_transpose.s` | Contiguous 8×64→16 transposed YMMs (tail finalization) |
| `md5x8_load_transpose_amd64.s` | VPINSRD scalar load+transpose (~288 insn/chunk, correct fallback) |
| `md5x8_amd64.go` | Go-side glue: `md5Hash8wayAVX2`, `md5Finalize8way` |
| `md5x8_common.go` | Shared MD5 constants + `md5FinalLane` |
| `md5x8_generic.go` | Stubs for non-amd64 (darwin/arm64) |
| `md5x8_purego.go` | Correct pure-Go 8-way MD5 reference (fallback / validation) |
| `md5x8_gather_amd64.s` | VPGATHERDD gather load + transpose (8-way AVX2) |
| `md5x16_amd64.s` | **Generated** — AVX-512 MD5 core (16-way, ≥2KB blocks) |
| `md5x16_amd64.go` | Go-side glue for AVX-512 path |
| `md5x16_gather_amd64.s` | ZMM VPGATHERDD load+transpose (k-mask reloaded per gather) |
| `sha256x8_amd64.s` | **Generated** — AVX2 SHA-256 core (8-way) |
| `sha256x8_amd64.go` | Go-side glue for SHA-256 8-way |
| `sha256x8_common.go` | Shared SHA-256 constants |
| `registry_stdlib.go` | Built-in hash constructors + `FastSum` implementations |
| `md5x8_test.go` | Tests: 8-way + 16-way MD5 parity, gather verification |
| `md5x8_rand_test.go` | Randomized MD5 parity (100 random-length blocks) |
| `delta_test.go` | Core roundtrip, identical-file, reconstruction tests |
| `fuzz_test.go` | Fuzz tests: roundtrip, wire encode/decode, checksum parity |
| `gen_md5x8/main.go` | Code generator for `md5x8_amd64.s` |
| `gen_md5x16/main.go` | Code generator for `md5x16_amd64.s` |
| `gen_sha256x8/main.go` | Code generator for `sha256x8_amd64.s` |
| `docs/checksum-engine.md` | Checksum engine: algorithm, loop structure, conventions, optimization history, SSE2 appendix |
| `docs/md5-simd.md` | MD5 SIMD reference: architecture, techniques, safety checklist |

## 📚 Documentation

- **[Checksum Engine](docs/checksum-engine.md)** — Rolling checksum algorithm, AVX2/SSE2 loop structure, Go Plan 9 conventions, optimization history (v0→v6), register map, test coverage, performance data.
- **[MD5 SIMD](docs/md5-simd.md)** — AVX2/AVX-512 parallel MD5 architecture, gather/transpose techniques, assembly notes, safety checklist.

## 🔗 Related

- [rsync](https://github.com/WayneD/rsync) — the original C implementation
- [md5-simd](https://github.com/minio/md5-simd) — MinIO's AVX2/AVX-512 MD5 (multi-stream server use case)
- [md5vec](https://github.com/igneous-systems/md5vec) — first Go AVX2 8-way MD5 (2018, unmaintained)
- [Shuttle](https://github.com/henryborner/shuttle) — Windows sync tool using this library
