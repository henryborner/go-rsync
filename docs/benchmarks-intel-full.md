# Intel Xeon 8269CY — full benchmark suite (raw archive, 2026-08-03)

Measured on ebmc6.26xlarge instance `120.26.249.55` (2026-08-03) with a
cross-compiled test binary, Go 1.26.5, current code (16-bit-lane +
conditional prefetch, main @ 59ccff3). A second instance was measured to
confirm the first one (116.62.156.48, since released); both matched within
±2% (session noise) — the numbers below are the newer instance.

Server: Intel Xeon Platinum 8269CY @2.5 GHz base / ~2.9 GHz sustained,
2 sockets × 26 cores (104 threads), Cascade Lake-SP, Ubuntu 26.04, Go 1.26.5.

## Full suite (count=3 medians)

| Benchmark | median |
|-----------|--------|
| ApplyDelta Match90 | 648 MB/s (1.619 ms) |
| ApplyDelta AllLiteral | 666 MB/s (1.573 ms) |
| RoundTrip | 119 MB/s (8.778 ms) |
| WireSignature Encode | 413 MB/s (134.2 µs) |
| WireSignature Decode | 284 MB/s (195.5 µs) |
| WireInstructions Encode | 1360 MB/s (771.1 µs) |
| WireInstructions Decode | 2857 MB/s (367.0 µs) |
| Signature (md5 1MB) | 1.62 GB/s (647.1 µs) |
| SignatureSHA256 | 0.34 GB/s (3.052 ms) |
| SignatureXXH64 | 3.42 GB/s (306.6 µs) |
| SignatureXXH3 | 4.15 GB/s (253.0 µs) |
| SignatureReader 10MB_700B | 1668 MB/s |
| SignatureReader 10MB_32KB | 2963 MB/s |
| SignatureReader 10MB_128KB | 2448 MB/s |
| SignatureReader 100MB_700B | 1504 MB/s |
| SignatureReader 100MB_128KB | 2347 MB/s |
| Search | 6.41 ms |
| SearchMiss | 6.39 ms |
| SearchReader | 8.12 ms |
| SearchParallel 1/2/4/8w | 6.40 / 4.18 / 2.61 / 1.58 ms |
| RollOnly / RollAndValue | 2.83 / 3.16 ns |
| Checksum1 1KB/8KB/64KB/1MB | 32.5 / 45.6 / 47.2 / 40.9 GB/s |
| MD5x8_Bulk | 2543 MB/s |
| MD5x8Core_Bulk | 3891 MB/s |
| MD5x16Core_Bulk | 10575 MB/s |
| SignatureParallel 1MB/10MB/100MB | 4.87 / 18.9 / 61.5 GB/s |

Notes:
- `-104` suffix = GOMAXPROCS 104. SignatureParallel uses GOMAXPROCS workers,
  so 100 MB (61.5 GB/s) reflects 104 threads vs the AMD 44.3 GB/s at 32
  threads — NOT an apples-to-apples comparison.
- MD5x16Core_Bulk (AVX-512): Intel 10.58 GB/s vs AMD 11.24 GB/s — near parity.

## vs-rsync (csumdiag, alternating, 500 ms time-box, 5 rounds, median)

go-rsync peak 50.0 GB/s (16 KB); the 4 KB dip is gone (45.6 GB/s). rsync
peak 36.4 GB/s. go-rsync leads ~30–42% at 8 KB+ (1 MB 15–30%).

## AVX-512 rolling checksum experiment

Single-ZMM 64 B/iter rolling checksum (`Checksum1AVX512`, ~10 insns/loop vs
AVX2 15/16), parity-verified (64 B–1 MB). csumdiag time-boxed comparison,
AVX2 vs AVX-512 (mode 0 vs 4):

| Block | AMD AVX2 | AMD AVX512 | Intel AVX2 | Intel AVX512 |
|-------|:--------:|:----------:|:----------:|:------------:|
| 1 KB | 74.3 | 48.7 | 30.6 | 21.7 |
| 8 KB | 101.3 | 92.8 | 47.5 | 46.8 |
| 16 KB | 105.2 | 98.4 | 50.1 | 54.0 |
| 32 KB | 105.4 | 98.7 | 48.9 | 56.0 |
| 64 KB | 104.5 | 101.1 | 47.3 | 58.9 |
| 128 KB | 106.1 | 102.6 | 48.3 | 61.0 |
| 256 KB | 106.4 | 104.5 | 48.8 | 62.0 |
| 1 MB | 100.2 | 104.1 | 40.5 | 43.9 |

Conclusion: on Intel (Cascade Lake full-width 512-bit units) ZMM wins +8–27%
from 16 KB up; on Zen 4 ZMM loses everywhere (−2 to −34%). AVX-512 rolling
checksum stays off for AMD; the opt-in `Checksum1AVX512` is exposed for users
who benchmark it faster on their own Intel server hardware. Not guaranteed on
all Intel CPUs — benchmark on your own hardware before enabling.
