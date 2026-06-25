# go-rsync

[![Go Reference](https://pkg.go.dev/badge/github.com/henryborner/go-rsync.svg)](https://pkg.go.dev/github.com/henryborner/go-rsync)
[![Go](https://img.shields.io/badge/Go-1.21+-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**Go implementation of the rsync delta-transfer algorithm** — rolling checksum matching, block signature generation, file reconstruction, and a binary wire protocol. Batteries included.

Used in production by [Shuttle](https://github.com/henryborner/shuttle), a Windows-native file sync tool.

## ✨ Features

- **⚡ 3-tier checksum engine** — AVX2 assembly (64B/iter) → SSE2/SSSE3 (32B/iter) → pure Go 128B batch. Auto-detects CPU at runtime.
- **🔌 Pluggable strong hash** — md5, sha256, xxh64 built-in. Register your own.
- **📡 Binary wire protocol** — compact big-endian encoding, ready for SSH pipes.
- **💧 Streaming I/O** — generate signatures from `io.Reader`, decode instructions one-at-a-time, minimal memory.
- **🔗 rsync-compatible checksum** — same CHAR_OFFSET=31, same uint32 natural-overflow arithmetic.
- **🧪 Well tested** — roundtrip, identical-file, partial-modification, and tier comparison tests.

## 📦 Install

```bash
go get github.com/henryborner/go-rsync
```

## 🚀 Quick start

```go
package main

import (
    "os"
    "github.com/henryborner/go-rsync"
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

**checksum1 throughput** on AMD Ryzen 9 8940HX (higher is better):

| Data size | AVX2 | SSE2 | Pure Go |
|-----------|------|------|---------|
| 1 KB | **45 GB/s** | 21 GB/s | 1.9 GB/s |
| 64 KB | **52 GB/s** | 26 GB/s | 1.9 GB/s |
| 1 MB | **52 GB/s** | 26 GB/s | 1.9 GB/s |

**AVX2 is ~27× faster than pure Go.** All tiers produce bit-identical results (parity tested).

Run on your own machine:

```bash
go test -bench BenchmarkAllTiers -benchmem ./...
```

## 📁 Package layout

| File | Purpose |
|------|---------|
| `rolling.go` | Rolling checksum (`RollingSum`, `Checksum1`) |
| `rolling_amd64.s` | AVX2 assembly (64B/iter, `VPMADDUBSW` core) |
| `rolling_sse2_amd64.s` | SSE2/SSSE3 assembly (32B/iter) |
| `rolling_fast_amd64.go` | Tiered dispatch: AVX2 → SSE2 → Go |
| `rolling_generic.go` | Portable pure-Go checksum |
| `match.go` | Block matching engine, signature generation |
| `reconstruct.go` | File reconstruction from instruction stream |
| `wire.go` | Binary wire protocol encode/decode |
| `registry.go` | Pluggable strong-hash registry |

## 🔗 Related

- [rsync](https://github.com/WayneD/rsync) — the original
- [librsync](https://github.com/librsync/librsync) — C library
- [Shuttle](https://github.com/henryborner/shuttle) — Windows sync tool using this library

## 📄 License

MIT © 2026 henryborner
