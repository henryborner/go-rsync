# go-rsync

[![Go Reference](https://pkg.go.dev/badge/github.com/henryborner/go-rsync.svg)](https://pkg.go.dev/github.com/henryborner/go-rsync)
[![Go](https://img.shields.io/badge/Go-1.26+-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**Go implementation of a binary delta-transfer algorithm** — rolling checksum matching, block signature generation, file reconstruction, and a binary wire protocol. The SIMD strong-hash engine (8-way AVX2, 16-way AVX-512, 4-way NEON MD5) lives in the separate [`hashsimd`](hashsimd/) module.

Built to power [Shuttle](https://github.com/henryborner/shuttle), my own Windows-native file sync tool — this library was extracted from Shuttle and is its core delta-transfer engine.

## Features

- **SIMD MD5 in the `hashsimd` submodule** — 8-way AVX2 (YMM, VPGATHERDD) + 16-way AVX-512 (ZMM) + 4-way ARM64 NEON parallel MD5, independently reusable.
- **Tiered SIMD checksum engine** — amd64: AVX2 (64B/iter) → SSE2 (32B/iter) → pure Go 128B batch; arm64: NEON UDOT → VUMULL → Go. Auto-detects CPU at runtime; opt-in AVX-512 (`Checksum1AVX512`).
- **Pluggable strong hash** — md5, sha256, xxh64, xxh3-128 built-in. Register your own with `FastSum` support.
- **Binary wire protocol** — compact big-endian encoding, ready for SSH pipes.
- **Streaming I/O** — `GenerateSignatureReader`, `SearchReader`, stream decode — O(blockSize) memory for multi-GB files.
- **Parallel APIs** — `GenerateSignatureParallel`, `SearchParallel`, `SearchReaderParallel` (near-linear speedup on many cores; streaming variant is O(windowSize) memory).
- **Rolling checksum** — CHAR_OFFSET=31, uint32 natural-overflow arithmetic.
- **Well tested** — roundtrip, fuzz, parity (AVX2 vs SSE2 vs pure Go), MD5 8-way + 16-way (AVX2 + AVX-512 vs stdlib).

## 📦 Install

```bash
go get github.com/henryborner/go-rsync
go get github.com/henryborner/go-rsync/hashsimd   # optional: SIMD MD5 engine
```

The repo is a Go workspace: the delta core (`github.com/henryborner/go-rsync`)
depends on `github.com/henryborner/go-rsync/hashsimd` for the SIMD MD5 fast
paths. `hashsimd` can also be used standalone for hashing many small blocks.

> The package is named **`delta`** (not `go-rsync`): `import delta "github.com/henryborner/go-rsync"`.

## 🔄 Relationship to rsync

go-rsync is an **embeddable delta-transfer library**, not an rsync client or
CLI. It reuses rsync's rolling-checksum matching ideas but is **not
wire-compatible** with the rsync protocol: its weak checksum uses
`CHAR_OFFSET=31`, while stock rsync uses 0, so the two cannot talk to each
other. If you need to interoperate with real rsync servers/clients, use
[gokrazy/rsync](https://github.com/gokrazy/rsync) instead. Reach for go-rsync
when you want to embed SIMD-accelerated binary delta into your own tool with
a small, streaming, dependency-light API.

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
```

For network use, split into sender/receiver:

```go
// --- Sender side ---
insts := delta.Delta(oldFile, newFile, blockSize, "md5")
delta.WireEncodeInstructions(conn, insts)

// --- Receiver side ---
delta.ApplyDeltaStream(oldFile, conn, outputFile, blockSize, "md5")
```

## 📊 Benchmarks

All numbers below: **AMD Ryzen 9 8940HX (Zen 4, 16 cores / 32 threads)**, Windows 11, Go 1.26.5.

**GenerateSignature (1MB data, blockSize=700, single-threaded):**

| Hash | Path | Time | Throughput |
| ----------- | ----------- | ------ | ------------ |
| md5 | AVX2 8-way | ~341 µs | **2.93 GB/s** |
| xxh64 | cespare/xxhash | ~151 µs | 6.62 GB/s |
| xxh3 | zeebo/xxh3 | ~116 µs | 8.62 GB/s |
| sha256 | stdlib SHA-NI | ~616 µs | 1.62 GB/s |

**GenerateSignatureParallel** (md5, 100MB, blockSize≈10KB, 32-thread): **44.3 GB/s** (AVX2 8-way, ~2.37 ms).

> `GenerateSignature` uses stdlib `crypto/sha256` (SHA-NI hardware path when
> available, falling back to its built-in AVX2 path otherwise).

**MD5 SIMD cores (zero-alloc):**

> Benchmarks below live in the `hashsimd/` submodule (`go test -bench=... ./hashsimd/`).

| Benchmark | Time | Throughput |
| ----------- | ------ | ------------ |
| `MD5x8_Bulk` (AVX2 8-way, 32KB) | ~7.7 µs | **4.26 GB/s** |
| `MD5x8Core_Bulk` (AVX2 raw, 1000×64B×8) | ~81 µs | 6.31 GB/s |
| `MD5x16Core_Bulk` (AVX-512 raw, 1000×64B×16) | ~91 µs | **11.24 GB/s** |

**Checksum1 (rolling weak checksum, AVX2 path):**

| Data size | Throughput |
| ----------- | :---: |
| 1 KB | **78 GB/s** |
| 64 KB | **109 GB/s** |
| 1 MB | **106 GB/s** |

> **Opt-in AVX-512**: `Checksum1AVX512(data []byte)` forces the ZMM path.
> Faster than AVX2 only on some Intel server Xeons for blocks ≥ 16 KB
> (up to +27%); slower on AMD, falls back without AVX-512. Benchmark on
> your own hardware before enabling.

Run on your own machine:

```bash
go test -bench='BenchmarkSignature$|BenchmarkSignatureXXH64$|BenchmarkSignatureXXH3$|BenchmarkSignatureSHA256$|BenchmarkSignatureParallel$|BenchmarkChecksum1$|BenchmarkSignatureReader$' -benchmem -count=3 .
go test -bench='BenchmarkMD5x8_Bulk$|BenchmarkMD5x8Core_Bulk$|BenchmarkMD5x16Core_Bulk$' -benchmem -count=3 ./hashsimd/
```

## 📁 Package layout

Two-module workspace:

- **`delta`** (this module) — `api.go` (high-level API), `match.go` (matching + signature generation), `wire.go` (binary protocol), `reconstruct.go`, `registry.go` (pluggable strong hashes), `rolling*.go/.s` (SIMD rolling checksum).
- **`hashsimd`** — SIMD MD5 engine (8-way AVX2 / 16-way AVX-512 / 4-way NEON), independently usable.

## 🔗 Related

- [rsync](https://github.com/WayneD/rsync) — the original C implementation
- [md5-simd](https://github.com/minio/md5-simd) — MinIO's AVX2/AVX-512 MD5 (multi-stream server use case)
- [gokrazy/rsync](https://github.com/gokrazy/rsync) — wire-compatible Go rsync client/server (use this for real rsync interop)
- [Shuttle](https://github.com/henryborner/shuttle) — Windows sync tool using this library
