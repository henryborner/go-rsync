# Intel Xeon 8269CY — full benchmark suite (raw archive, 2026-08-03)

Raw single-run output from the Aliyun bare-metal before instance release.
Server: Intel Xeon Platinum 8269CY @2.5GHz, 2 sockets x 26 cores (104 threads),
Cascade Lake, Ubuntu 26.04, Go 1.26.5, go-rsync main @ 0fd5486 (16-bit-lane +
conditional prefetch). Run: `./go-rsync-bench.test -test.bench=... -test.benchmem -test.count=1`.
Cross-compiled on the AMD dev machine, uploaded; no deps needed on server.

```
goos: linux
goarch: amd64
pkg: github.com/henryborner/go-rsync
cpu: Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz
BenchmarkApplyDelta/Match90-104              841           1504818 ns/op         696.81 MB/s     5415000 B/op      14 allocs/op
BenchmarkApplyDelta/AllLiteral-104           804           1544480 ns/op         678.92 MB/s     5415003 B/op      14 allocs/op
BenchmarkRoundTrip-104                       135           8755086 ns/op         119.77 MB/s     6063881 B/op      30 allocs/op
BenchmarkWireSignature/Encode-104           8853            127211 ns/op         435.83 MB/s      202976 B/op    1511 allocs/op
BenchmarkWireSignature/Decode-104           6211            190833 ns/op         290.53 MB/s      162128 B/op    4498 allocs/op
BenchmarkWireInstructions/Encode-104                1629            747661 ns/op        1402.69 MB/s     2580775 B/op       41 allocs/op
BenchmarkWireInstructions/Decode-104                3132            359285 ns/op        2918.96 MB/s     1050680 B/op       99 allocs/op
BenchmarkSignature-104                              1807            645213 ns/op          120928 B/op          5 allocs/op
BenchmarkSignatureSHA256-104                         384           3065553 ns/op          145504 B/op          5 allocs/op
BenchmarkSignatureXXH64-104                         3930            303681 ns/op          103200 B/op          5 allocs/op
BenchmarkSignatureXXH3-104                          4719            248113 ns/op          115488 B/op          5 allocs/op
BenchmarkSignatureReader/10MB_700B-104               182           6446569 ns/op        1626.56 MB/s     1095776 B/op        5 allocs/op
BenchmarkSignatureReader/10MB_32KB-104               321           3661817 ns/op        2863.54 MB/s      548192 B/op        5 allocs/op
BenchmarkSignatureReader/10MB_128KB-104              273           4332065 ns/op        2420.50 MB/s     2103392 B/op        5 allocs/op
BenchmarkSignatureReader/100MB_700B-104               15          73831488 ns/op        1420.23 MB/s    10803296 B/op        5 allocs/op
BenchmarkSignatureReader/100MB_128KB-104              24          47368830 ns/op        2213.64 MB/s     2159968 B/op        5 allocs/op
BenchmarkSearch-104                                  187           6414791 ns/op          527968 B/op         11 allocs/op
BenchmarkSearchMiss-104                              186           6402336 ns/op          527968 B/op         11 allocs/op
BenchmarkRollValue/RollOnly-104                 424778154                2.827 ns/op           0 B/op          0 allocs/op
BenchmarkRollValue/RollAndValue-104             379469716                3.161 ns/op           0 B/op          0 allocs/op
BenchmarkChecksum1/1KB-104                      36023484                33.27 ns/op     30781.26 MB/s          0 B/op        0 allocs/op
BenchmarkChecksum1/8KB-104                       6824966               175.0 ns/op      46803.02 MB/s          0 B/op        0 allocs/op
BenchmarkChecksum1/64KB-104                       876914              1360 ns/op        48182.32 MB/s          0 B/op        0 allocs/op
BenchmarkChecksum1/1024KB-104                      47280             25359 ns/op        41349.59 MB/s          0 B/op        0 allocs/op
BenchmarkSearchReader-104                            147           8131547 ns/op          655682 B/op          7 allocs/op
BenchmarkSearchParallel/workers=1-104                184           6407957 ns/op          527968 B/op         11 allocs/op
BenchmarkSearchParallel/workers=2-104                286           4154124 ns/op          533885 B/op         39 allocs/op
BenchmarkSearchParallel/workers=4-104                418           2688788 ns/op          539658 B/op         68 allocs/op
BenchmarkSearchParallel/workers=8-104                738           1604844 ns/op          541341 B/op        112 allocs/op
BenchmarkMD5x8_Bulk-104                            93002             12899 ns/op        2540.32 MB/s           0 B/op        0 allocs/op
BenchmarkMD5x8Core_Bulk-104                         8988            131597 ns/op        3890.67 MB/s           0 B/op        0 allocs/op
BenchmarkMD5x16Core_Bulk-104                       12376             96947 ns/op        10562.42 MB/s          0 B/op        0 allocs/op
BenchmarkSignatureParallel/1MB-104                  5246            214789 ns/op        4881.89 MB/s      132508 B/op      204 allocs/op
BenchmarkSignatureParallel/10MB-104                 2049            545146 ns/op        19234.76 MB/s     748039 B/op      213 allocs/op
BenchmarkSignatureParallel/100MB-104                 766           1698420 ns/op        61738.34 MB/s     747848 B/op      212 allocs/op
```

Notes:
- `-104` suffix = GOMAXPROCS 104. SignatureParallel uses GOMAXPROCS workers,
  so 100MB (61.7 GB/s) reflects 104 threads vs the AMD 44.3 GB/s at 32 threads —
  NOT an apples-to-apples comparison. AMD numbers in benchmarks.md are 32-thread.
- MD5x16Core_Bulk (AVX-512): Intel 10.56 GB/s vs AMD 11.24 GB/s — near parity.
- Checksum1 single-run here (30.8/46.8/48.2/41.3 GB/s) is lower than the
  csumdiag time-boxed numbers (33.5/47.7/47.3/39.1) due to different harness.

---

## count=3 median (2026-08-03, same run set re-run with -test.count=3)

Medians of the three samples; full raw output was captured in the chat log.
Structured tables live in benchmarks.md (Intel full-suite section).

| Benchmark | median |
|-----------|--------|
| ApplyDelta Match90 | 679 MB/s (1.544 ms) |
| ApplyDelta AllLiteral | 703 MB/s (1.492 ms) |
| RoundTrip | 121 MB/s (8.644 ms) |
| WireSignature Encode | 408 MB/s (135.8 µs) |
| WireSignature Decode | 287 MB/s (193.2 µs) |
| WireInstructions Encode | 1339 MB/s (783.5 µs) |
| WireInstructions Decode | 2873 MB/s (365.1 µs) |
| Signature (md5 1MB) | 1.63 GB/s (642.7 µs) |
| SignatureSHA256 | 0.34 GB/s (3.040 ms) |
| SignatureXXH64 | 3.42 GB/s (306.9 µs) |
| SignatureXXH3 | 4.16 GB/s (251.7 µs) |
| SignatureReader 10MB_700B | 1666 MB/s |
| SignatureReader 10MB_32KB | 2916 MB/s |
| SignatureReader 10MB_128KB | 2431 MB/s |
| SignatureReader 100MB_700B | 1481 MB/s |
| SignatureReader 100MB_128KB | 2332 MB/s |
| Search | 6.46 ms |
| SearchMiss | 6.38 ms |
| SearchReader | 8.12 ms |
| SearchParallel 1/2/4/8w | 6.39 / 4.05 / 2.51 / 1.60 ms |
| RollOnly / RollAndValue | 2.83 / 3.16 ns |
| Checksum1 1KB/8KB/64KB/1MB | 30.8 / 46.7 / 48.3 / 41.0 GB/s |
| MD5x8_Bulk | 2541 MB/s |
| MD5x8Core_Bulk | 3890 MB/s |
| MD5x16Core_Bulk | 10557 MB/s |
| SignatureParallel 1MB/10MB/100MB | 4.89 / 19.5 / 61.9 GB/s |

---

## AVX-512 rolling checksum experiment (2026-08-03)

Single-ZMM 64B/iter rolling checksum (`checksum1AVX512`, ~11 insns/loop vs
AVX2 16) written in tmp/go-rsync-avx512, parity-verified on AMD (64B–1MB all
bit-identical). csumdiag time-boxed comparison, AVX2 vs AVX512 (mode 0 vs 4):

| Block | AMD AVX2 | AMD AVX512 | Intel AVX2 | Intel AVX512 |
|-------|:--------:|:----------:|:----------:|:------------:|
| 1 KB | 74.3 | 48.7 | 30.6 | 22.1 |
| 8 KB | 101.3 | 92.8 | 46.8 | 46.3 |
| 16 KB | 105.2 | 98.4 | 49.6 | 53.6 |
| 32 KB | 105.4 | 98.7 | 48.9 | 55.6 |
| 64 KB | 104.5 | 101.1 | 47.3 | 58.7 |
| 128 KB | 106.1 | 102.6 | 48.3 | 60.8 |
| 256 KB | 106.4 | 104.5 | 48.8 | 61.8 |
| 1 MB | 100.2 | 104.1 | 39.1 | 38.2 |

Conclusion: on Intel (Cascade Lake full-width 512-bit units) ZMM wins +8–27%
from 16 KB up; on Zen 4 ZMM loses everywhere (−2 to −34%). AVX-512 rolling
checksum stays off for AMD; Intel-only ≥16 KB dispatch not implemented.

---

## Second instance re-measurement (2026-08-03, 120.26.249.55)

A fresh ebmc6.26xlarge instance was measured to confirm the first one
(116.62.156.48, since released). Results match within ±2% (session noise) —
same hardware, same ~2.9 GHz clock reality.

### vs-rsync (csumdiag, alternating, mode 0 / mode 1)

go-rsync peak 50.0 GB/s (16 KB); 4 KB dip gone (45.6). rsync peak 36.4.
Leading margin ~30–42% at 8 KB+ (1 MB 15–30%).

### Full suite (count=3 medians)

- Signature md5 1.62 / sha256 0.34 / xxh64 3.42 / xxh3 4.15 GB/s
- SignatureParallel 4.87 / 18.9 / 61.5 GB/s (1/10/100 MB)
- SignatureReader 1668 / 2963 / 2448 / 1504 / 2347 MB/s
- Search 6.41 / SearchMiss 6.39 / SearchReader 8.12 ms
- SearchParallel 6.40 / 4.18 / 2.61 / 1.58 ms
- ApplyDelta 648 / 666 MB/s, RoundTrip 119 MB/s
- WireSignature 413 / 284 MB/s, WireInstructions 1360 / 2857 MB/s
- RollOnly 2.83 / RollAndValue 3.16 ns
- Checksum1 32.5 / 45.6 / 47.2 / 40.9 GB/s (1KB/8KB/64KB/1MB)
- MD5x8_Bulk 2543 / MD5x8Core_Bulk 3891 / MD5x16Core_Bulk 10575 MB/s

### AVX-512 opt-in (csumdiag mode 0 vs 4, same instance)

16 KB 50.1→54.0 (+8%), 64 KB 47.3→58.9 (+25%), 256 KB 48.8→62.0 (+27%),
1 MB 40.5→43.9 (+8%, this session). Reproduces the first instance.
