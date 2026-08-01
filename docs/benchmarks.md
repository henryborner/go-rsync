# go-rsync Benchmarks

> Complete benchmark results with per-operation allocation counts. Measured on
> an **AMD Ryzen 9 8940HX (Zen 4)** running Windows 11, Go 1.26.5. Each value
> is the median of 3 runs (`-count=3 -benchmem`).

## Hardware

| | |
|---|---|
| CPU | AMD Ryzen 9 8940HX (Zen 4, 8 cores / 16 threads) |
| L3 cache | 40 MB (full) |
| OS / Go | Windows 11 / go1.26.5 |

## GenerateSignature (1 MB data, single-threaded)

| Algorithm | Time | Throughput | B/op | allocs/op |
|-----------|------|-----------|------|-----------|
| md5 | ~341 µs | 2.93 GB/s | 120,928 | 5 |
| sha256 | ~616 µs | 1.62 GB/s | 140,064 | 5 |
| xxh64 | ~151 µs | 6.62 GB/s | 103,200 | 5 |
| xxh3 | ~116 µs | 8.62 GB/s | 115,488 | 5 |

## GenerateSignatureParallel

| Data | Time | Throughput | B/op | allocs/op |
|------|------|-----------|------|-----------|
| 1 MB | ~129 µs | 8.1 GB/s | 120,386 | 68 |
| 10 MB | ~389 µs | 26.9 GB/s | 734,784 | 68 |
| 100 MB | ~2.37 ms | 44.3 GB/s | 734,794 | 68 |

## SignatureReader (streaming)

| Config | Time | Throughput | B/op | allocs/op |
|--------|------|-----------|------|-----------|
| 10MB_700B | ~3.30 ms | 3.18 GB/s | 1,095,776 | 5 |
| 10MB_32KB | ~3.22 ms | 3.26 GB/s | 548,192 | 5 |
| 10MB_128KB | ~3.30 ms | 3.18 GB/s | 2,103,392 | 5 |
| 100MB_700B | ~33.4 ms | 3.15 GB/s | 10,803,296 | 5 |
| 100MB_128KB | ~32.3 ms | 3.25 GB/s | 2,159,968 | 5 |

## Checksum1 (rolling weak checksum — zero-alloc)

| Size | Time | Throughput | B/op | allocs/op |
|------|------|-----------|------|-----------|
| 1 KB | ~16.0 ns | 64.2 GB/s | 0 | 0 |
| 8 KB | ~104 ns | 79.0 GB/s | 0 | 0 |
| 64 KB | ~817 ns | 80.2 GB/s | 0 | 0 |
| 1 MB | ~13.1 µs | 80.4 GB/s | 0 | 0 |

## MD5 SIMD cores (zero-alloc)

| Benchmark | Time | Throughput | B/op | allocs/op |
|-----------|------|-----------|------|-----------|
| MD5x8_Bulk (AVX2, 32 KB) | ~7.7 µs | 4.26 GB/s | 0 | 0 |
| MD5x8Core_Bulk (AVX2 raw) | ~81 µs | 6.31 GB/s | 0 | 0 |
| MD5x16Core_Bulk (AVX-512 raw) | ~91 µs | 11.24 GB/s | 0 | 0 |

## Notes

- **`0 B/op` / `0 allocs/op`** means the operation performs **zero heap
  allocations** — it fully reuses caller buffers and never triggers the GC.
  `Checksum1` and the MD5/SHA-256 SIMD cores are zero-alloc by design.
- `GenerateSignature` allocates once per call for the `BlockSums` result slice
  (~103–140 KB for 1 MB input, 5 allocs).
- `SignatureReader` allocates its streaming buffers (~548 KB–10.8 MB depending
  on block size / file size, fixed 5 allocs/op).
- ARM64 NEON results (Checksum1 UDOT/VUMULL, 4-way MD5) are measured on ARM64
  CI (ubuntu-24.04-arm); see [neon-checksum.md](neon-checksum.md).

## Reproduce

```bash
go test -bench='BenchmarkSignature$|BenchmarkSignatureXXH64$|BenchmarkSignatureXXH3$|BenchmarkSignatureSHA256$|BenchmarkMD5x8_Bulk$|BenchmarkMD5x8Core_Bulk$|BenchmarkMD5x16Core_Bulk$|BenchmarkChecksum1$|BenchmarkSignatureReader$' -benchmem -count=3 .
```
