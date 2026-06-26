# go-rsync

[![Go Reference](https://pkg.go.dev/badge/github.com/henryborner/go-rsync.svg)](https://pkg.go.dev/github.com/henryborner/go-rsync)
[![Go](https://img.shields.io/badge/Go-1.25+-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**Go implementation of the rsync delta-transfer algorithm** — the first Go rsync library with AVX2/AVX-512 accelerated MD5 (8-way + 16-way SIMD). Rolling checksum matching, block signature generation, file reconstruction, and a binary wire protocol. Batteries included.

Used in production by [Shuttle](https://github.com/henryborner/shuttle), a Windows-native file sync tool.

## ✨ Features

- **🧬 8-way AVX2 MD5** — 8 blocks hashed in parallel via hand-written AVX2 assembly (YMM registers), VPGATHERDD gather load (raw machine code, bypasses Go assembler VSIB bug). First Go rsync library to do this.
- **⚡ 3-tier checksum engine** — AVX2 (64B/iter) → SSE2/SSSE3 (32B/iter) → pure Go 128B batch. Auto-detects CPU at runtime.
- **🔌 Pluggable strong hash** — md5, sha256, xxh64 built-in. Register your own with `FastSum` support.
- **📡 Binary wire protocol** — compact big-endian encoding, ready for SSH pipes.
- **💧 Streaming I/O** — generate signatures from `io.Reader`, decode instructions one-at-a-time, minimal memory.
- **🔗 rsync-compatible checksum** — same CHAR_OFFSET=31, same uint32 natural-overflow arithmetic.
- **🧪 Well tested** — roundtrip, identical-file, parity tests (AVX2 vs SSE2 vs pure Go), MD5 8-way validation.

## 📦 Install

```bash
go get github.com/henryborner/go-rsync
```

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

    // 1. Generate signature from old file
    sig := delta.GenerateSignature(oldFile, blockSize, "md5")

    // 2. Search new file for matching blocks
    eng := delta.NewMatchEngine(blockSize, "md5")
    eng.LoadSignature(sig)
    instructions := eng.Search(newFile)

    // 3. Reconstruct on the other side
    recon := delta.NewReconstructor(oldFile, blockSize, "md5")
    result, _ := recon.Reconstruct(instructions)

    os.WriteFile("v2_reconstructed.bin", result, 0644)
}
```

## 📊 Benchmarks

**AMD Ryzen 9 8940HX (Zen 4), single-threaded, 1MB data, blockSize≈700:**

| Benchmark | Time | Throughput | Allocs |
|-----------|------|------------|--------|
| `GenerateSignature` (md5) | ~447 µs | **2.35 GB/s** | 3 |
| `GenerateSignature` (xxh64) | ~318 µs | 3.30 GB/s | — |
| `GenerateSignature` (sha256) | ~819 µs | 1.28 GB/s | — |
| `MD5x8_Bulk` (raw AVX2 core) | 10516 ns/op | **5950 MB/s** | 0 |
| `MD5x16_Bulk` (raw AVX-512 core) | — | **10500 MB/s** | 0 |

**Checksum1 (rolling weak checksum) throughput:**

| Data size | AVX2 (Ryzen) | AVX2 (Xeon) | rsync-AVX2 (Xeon) |
|-----------|:---:|:---:|:---:|
| 1 KB | **55 GB/s** | 37 GB/s | 43 GB/s |
| 64 KB | **69 GB/s** | 44 GB/s | 44 GB/s |
| 1 MB | **51 GB/s** | 44 GB/s | — |

> 64KB within 1% of rsync on Xeon. 1KB gap due to rsync's VPSRLD/VPSRLDQ exit-correction approach.
> CHAR_OFFSET + packing done in assembly via `checksum1PackedAVX2`.

Run on your own machine:

```bash
go test -bench='BenchmarkSignature$|BenchmarkMD5x8_Bulk|BenchmarkChecksum1' -benchmem .
```

## 📁 Package layout

| File | Purpose |
|------|---------|
| `match.go` | Block matching engine, signature generation |
| `reconstruct.go` | File reconstruction from instruction stream |
| `wire.go` | Binary wire protocol encode/decode |
| `registry.go` | Pluggable strong-hash registry |
| `rolling.go` | Rolling checksum (`RollingSum`, `Checksum1`) |
| `rolling_amd64.s` | AVX2 checksum assembly (64B/iter) + `checksum1PackedAVX2` |
| `rolling_sse2_amd64.s` | SSE2/SSSE3 checksum assembly (32B/iter) |
| `rolling_fast_amd64.go` | Tiered dispatch: AVX2 → SSE2 → Go, inlined `Checksum1` |
| `rolling_generic.go` | Portable pure-Go checksum (non-amd64) |
| `md5x8_amd64.s` | **Generated** — 64-step unrolled AVX2 MD5 core (8-way) |
| `md5x8_transpose_fast_amd64.s` | Register-shuffle transpose (~80 vs ~320 VPINSRD instructions) |
| `md5x8_transpose.s` | Contiguous 8×64→16 transposed YMMs (tail finalization only) |
| `md5x8_load_transpose_amd64.s` | VPINSRD scalar load+transpose (~288 insn/chunk, correct fallback) |
| `md5x8_transpose_fast_amd64.s` | Register-shuffle transpose (~80 vs ~320 VPINSRD instructions) |
| `md5x8_amd64.go` | Go-side glue: `md5Hash8wayAVX2`, `md5Finalize8way` |
| `md5x8_common.go` | Shared MD5 constants + `md5FinalLane` |
| `md5x8_generic.go` | Stubs for non-amd64 (darwin/arm64) |
| `md5x8_purego.go` | Correct pure-Go 8-way MD5 reference (fallback / validation) |
| `md5x8_gather_amd64.s` | **Raw machine code** — VPGATHERDD load+transpose (bypasses Go asm VSIB bug) |
| `md5x16_amd64.s` | **Generated** — AVX-512 MD5 core (16-way, ≥2KB blocks) |
| `md5x16_amd64.go` | Go-side glue for AVX-512 path |
| `md5x16_gather_amd64.s` | ZMM VPGATHERDD load+transpose |
| `gen_md5x8/main.go` | Code generator for `md5x8_amd64.s` |
| `gen_md5x16/main.go` | Code generator for `md5x16_amd64.s` |
| `docs/md5-avx2-notes.md` | Maintenance guide for the AVX2 MD5 acceleration |

## 🔗 Related

- [rsync](https://github.com/WayneD/rsync) — the original C implementation
- [md5-simd](https://github.com/minio/md5-simd) — MinIO's AVX2/AVX-512 MD5 (multi-stream server use case)
- [md5vec](https://github.com/igneous-systems/md5vec) — first Go AVX2 8-way MD5 (2018, unmaintained)
- [Shuttle](https://github.com/henryborner/shuttle) — Windows sync tool using this library
