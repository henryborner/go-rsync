# go-rsync

[![Go Reference](https://pkg.go.dev/badge/github.com/henryborner/go-rsync.svg)](https://pkg.go.dev/github.com/henryborner/go-rsync)

Go implementation of a binary delta-transfer algorithm: rolling checksum matching, block signature generation, file reconstruction, and a binary wire protocol. SIMD MD5 (8-way AVX2 / 16-way AVX-512 / 4-way NEON) lives in the separate `hashsimd` module.

Not an rsync client and not wire-compatible with the rsync protocol (`CHAR_OFFSET=31` vs 0) — for real interop use [gokrazy/rsync](https://github.com/gokrazy/rsync).

## Install

```bash
go get github.com/henryborner/go-rsync
go get github.com/henryborner/go-rsync/hashsimd   # optional: SIMD MD5
```

Package name is `delta` (`import delta "github.com/henryborner/go-rsync"`).

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

Network use — sender: `Delta` + `WireEncodeInstructions`; receiver: `ApplyDeltaStream`. Streaming (`GenerateSignatureReader`, `SearchReader`) and parallel (`GenerateSignatureParallel`, `SearchParallel`) variants keep memory at O(blockSize) for multi-GB files.

## 📊 Benchmarks (AMD Ryzen 9 8940HX, Go 1.26.5)

| Operation | Throughput |
| --------- | ----------: |
| Rolling `Checksum1` (AVX2) | 78–109 GB/s |
| `GenerateSignature` (md5, 1 MB) | 2.93 GB/s |
| `GenerateSignatureParallel` (md5, 100 MB, 32t) | 44.3 GB/s |
| MD5 SIMD core (AVX-512) | 11.24 GB/s |

## 📁 Package layout

Two-module workspace:

- **`delta`** (this module) — `api.go` (high-level API), `match.go` (matching + signature generation), `wire.go` (binary protocol), `reconstruct.go`, `registry.go` (pluggable strong hashes), `rolling*.go/.s` (SIMD rolling checksum).
- **`hashsimd`** — SIMD MD5 engine (8-way AVX2 / 16-way AVX-512 / 4-way NEON), independently usable.

## 🔗 Related

- [gokrazy/rsync](https://github.com/gokrazy/rsync) — wire-compatible Go rsync client/server (for real rsync interop)
