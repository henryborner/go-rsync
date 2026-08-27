# go-rsync Benchmarks

> Complete benchmark results with per-operation allocation counts. Two
> machines, each measured with Go 1.26.5 (`-count=3 -benchmem`, median):
>
> - **AMD Ryzen 9 8940HX (Zen 4)** — the local dev machine (primary).
> - **Intel Xeon 8269CY (Cascade Lake)** — rented Aliyun bare-metal,
>   **reference only**: no virtualization, but CPU frequency / Turbo may be
>   constrained by the cloud provider and results may drift between sessions.

## Hardware

### AMD Ryzen 9 8940HX (local, primary)

| | |
|---|---|
| CPU | AMD Ryzen 9 8940HX (Zen 4, 16 cores / 32 threads) |
| L3 cache | 64 MB (full) |
| OS / Go | Windows 11 / go1.26.5 (vs-rsync rows: WSL2 Ubuntu) |

### Intel Xeon 8269CY (Aliyun bare-metal, reference only)

| | |
|---|---|
| Aliyun spec | ecs.ebmc6.26xlarge (ebmc6 compute bare-metal, 104 vCPU / 192 GiB, 15 AZs) |
| CPU | Intel Xeon Platinum 8269CY, Cascade Lake-SP (family 6 / model 85 / stepping 7, microcode 0x5003901), 2.5 GHz base / 3.8 GHz max, TSX disabled |
| Cores | 2 sockets × 26 cores = 52 cores / 104 threads (2 threads/core) |
| L1 cache | 32K data + 32K instr per core (8-way) |
| L2 cache | 1 MiB per core (16-way) |
| L3 cache | 35.75 MiB per socket (11-way), 71.5 MiB total — `lscpu -C` ONE-SIZE=35.8M / ALL-SIZE=71.5M, sysfs `index3`=36608K |
| NUMA | 2 nodes (node0 0-25,52-77; node1 26-51,78-103) |
| Memory | 187 GiB usable (spec 192 GiB), DDR4-2666 ECC registered, Hynix 16 GB DIMMs (HMA82GR7AFR4N-VK); NUMA node0 92.6 GiB / node1 94.4 GiB (asymmetric) |
| Clock reality | sustained load runs at **~2.9 GHz** (`intel_cpufreq` + `performance` governor, Turbo on, but PL-limited) — all measurements here were taken at this frequency, **not** the 3.8 GHz spec |
| Disk | 20 GB system (vda, `/`, virtio cloud disk, rotational ROTA=1) |
| Network | virtio NIC (cloud IO virt; speed not exposed; CPU itself is bare-metal, `systemd-detect-virt` = none) |
| BMC | ASPEED AST1150 / ASPEED Graphics (out-of-band management) |
| OS / Go | Ubuntu 26.04 (kernel 7.0.0) / go1.26.5 |

## GenerateSignature (1 MB data, single-threaded, blockSize=700)

### AMD Ryzen 9 8940HX

| Algorithm | Path | Time | Throughput | B/op | allocs/op |
|-----------|------|------|-----------|------|-----------|
| md5 | AVX2 8-way (`hashsimd/md5x8_amd64.s`) | ~341 µs | 2.93 GB/s | 120,928 | 5 |
| sha256 | stdlib SHA-NI (`crypto/sha256`) | ~616 µs | 1.62 GB/s | 140,064 | 5 |
| xxh64 | cespare/xxhash | ~151 µs | 6.62 GB/s | 103,200 | 5 |
| xxh3 | zeebo/xxh3 | ~116 µs | 8.62 GB/s | 115,488 | 5 |

### Intel Xeon 8269CY (reference only)

| Algorithm | Throughput |
|-----------|:----------:|
| md5 (AVX2 8-way) | 1.62 GB/s |
| sha256 (SHA-NI) | 0.34 GB/s |
| xxh64 | 3.42 GB/s |
| xxh3 | 4.15 GB/s |

## GenerateSignatureParallel (md5)

This path has **no AVX-512 16-way branch** — md5 stays on 8-way AVX2 at every
block size. Note `SignatureParallel` uses GOMAXPROCS workers, so the Intel
(104 threads) and AMD (32 threads) rows are **not directly comparable**.

### AMD Ryzen 9 8940HX

| Data | blockSize | Path | Time | Throughput | B/op | allocs/op |
|------|-----------|------|------|-----------|------|-----------|
| 1 MB | 700 | AVX2 8-way | ~129 µs | 8.1 GB/s | 120,386 | 68 |
| 10 MB | 1048 | AVX2 8-way | ~389 µs | 26.9 GB/s | 734,784 | 68 |
| 100 MB | 10485 | AVX2 8-way | ~2.37 ms | 44.3 GB/s | 734,794 | 68 |

### Intel Xeon 8269CY (reference only, 104-thread GOMAXPROCS)

| Data | Throughput |
|------|:----------:|
| 1 MB | 4.87 GB/s |
| 10 MB | 18.9 GB/s |
| 100 MB | 61.5 GB/s |

The 100 MB reading (61.5 GB/s) is *higher* than AMD's 44.3 GB/s purely
because it fans out to 104 threads vs AMD's 32 — not a single-core win.

## SignatureReader (streaming, md5)

md5 dispatch: blockSize ≥ 2 KB → AVX-512 16-way, otherwise AVX2 8-way.

### AMD Ryzen 9 8940HX

| Config | Path | Time | Throughput | B/op | allocs/op |
|--------|------|------|-----------|------|-----------|
| 10MB_700B | AVX2 8-way | ~3.30 ms | 3.18 GB/s | 1,095,776 | 5 |
| 10MB_32KB | AVX-512 16-way | ~3.22 ms | 3.26 GB/s | 548,192 | 5 |
| 10MB_128KB | AVX-512 16-way | ~3.30 ms | 3.18 GB/s | 2,103,392 | 5 |
| 100MB_700B | AVX2 8-way | ~33.4 ms | 3.15 GB/s | 10,803,296 | 5 |
| 100MB_128KB | AVX-512 16-way | ~32.3 ms | 3.25 GB/s | 2,159,968 | 5 |

### Intel Xeon 8269CY (reference only)

| Config | Throughput |
|--------|:----------:|
| 10MB_700B | 1668 MB/s |
| 10MB_32KB | 2963 MB/s |
| 10MB_128KB | 2448 MB/s |
| 100MB_700B | 1504 MB/s |
| 100MB_128KB | 2347 MB/s |

## Search / delta matching (md5)

### AMD Ryzen 9 8940HX

#### Search (1 MB, blockSize=700)

| Benchmark | Data | Time | B/op | allocs/op |
|-----------|------|------|------|-----------|
| `Search` (90% match) | 1 MB, every 10th byte flipped | ~3.2 ms | — | — |
| `SearchMiss` (all-miss) | 1 MB, unrelated data | ~3.1 ms | — | — |
| `SearchReader` (streaming) | 1 MB | ~3.8 ms | 655,680 | 7 |

#### SearchMatrix (size × match density)

| Data | miss | match90 | identical |
|------|------|---------|-----------|
| 1 MB (blockSize 700) | 330 MB/s | 330 MB/s | 903 MB/s |
| 32 MB (blockSize ~2 KB) | 210 MB/s | 210 MB/s | 1066 MB/s |

- miss ≈ match90: flipping 10% of bytes breaks ~20% of blocks, so the damaged
  regions dominate and are scanned byte-by-byte in both cases.
- identical jumps by blockSize on every match (~0.9–1.1 GB/s).
- Large files drop to ~210 MB/s (data + table leave L2; TLB pressure).

#### SearchParallel (1 MB, md5)

| Workers | Time |
|---------|------|
| 1 | 3.1 ms |
| 2 | 1.8 ms |
| 4 | 1.2 ms |
| 8 | 1.0 ms |

### Intel Xeon 8269CY (reference only)

| Benchmark | Time |
|-----------|:-----:|
| `Search` (90% match) | 6.41 ms |
| `SearchMiss` | 6.39 ms |
| `SearchReader` | 8.12 ms |
| `SearchParallel` 1w | 6.40 ms |
| `SearchParallel` 2w | 4.18 ms |
| `SearchParallel` 4w | 2.61 ms |
| `SearchParallel` 8w | 1.58 ms |

## Delta pipeline (reconstruct / roundtrip)

### AMD Ryzen 9 8940HX

| Benchmark | Time (1 MB) | Throughput |
|-----------|-------------|-----------|
| `ApplyDelta` Match90 (~90% block refs) | ~371 µs | ~2.8 GB/s |
| `ApplyDelta` AllLiteral (all literals) | ~355 µs | ~3.0 GB/s |
| `RoundTrip` (Delta + ApplyDelta + verify) | ~4.19 ms | ~250 MB/s (search-dominated) |

### Intel Xeon 8269CY (reference only)

| Benchmark | Throughput |
|-----------|:----------:|
| `ApplyDelta` Match90 | 648 MB/s |
| `ApplyDelta` AllLiteral | 666 MB/s |
| `RoundTrip` | 119 MB/s |

## RollingSum hot path (Roll + Value)

~10 cycles/byte on AMD — the serial dependency floor of the search hot path;
the hash-table lookup latency (L2/L3) dominates the remaining cost per byte.

### AMD Ryzen 9 8940HX

| Benchmark | ns/op |
|-----------|-------|
| `RollValue`/RollOnly | ~1.84 ns |
| `RollValue`/RollAndValue | ~2.07 ns |

### Intel Xeon 8269CY (reference only)

| Benchmark | ns/op |
|-----------|:-----:|
| `RollOnly` | 2.83 |
| `RollAndValue` | 3.16 |

## Wire format

### AMD Ryzen 9 8940HX

| Stream | Encode | Decode |
|--------|--------|--------|
| Signature (1 MB, ~1500 blocks) | ~1.03 GB/s | ~656 MB/s |
| Instructions (typical 10%-modified delta) | ~5.5 GB/s | ~8.2 GB/s |

### Intel Xeon 8269CY (reference only)

| Stream | Encode | Decode |
|--------|:------:|:------:|
| Signature | 408 MB/s | 287 MB/s |
| Instructions | 1339 MB/s | 2873 MB/s |

## Checksum1 (rolling weak checksum — zero-alloc)

(v7 16-bit-lane rewrite 2026-08-02: 19→16 instructions, no VPMADDWD.
Conditional prefetch 2026-08-03: `PREFETCHT0` only for blocks ≥ 64 KB — the
384 B-ahead prefetch ran past the buffer end and measurably hurt
cache-resident sizes on Intel. Pre-v7: 64.2 / 79.0 / 80.2 / 80.4 GB/s.)

### AMD Ryzen 9 8940HX (go test harness)

| Size | Time | Throughput | B/op | allocs/op |
|------|------|-----------|------|-----------|
| 1 KB | ~13.1 ns | 78.1 GB/s | 0 | 0 |
| 8 KB | ~75.7 ns | 108.2 GB/s | 0 | 0 |
| 64 KB | ~600 ns | 109.3 GB/s | 0 | 0 |
| 1 MB | ~9.93 µs | 105.6 GB/s | 0 | 0 |

### Intel Xeon 8269CY (go test harness, reference only)

| Size | Throughput |
|------|:----------:|
| 1 KB | 32.5 GB/s |
| 8 KB | 45.6 GB/s |
| 64 KB | 47.2 GB/s |
| 1 MB | 40.9 GB/s |

### AVX-512 rolling checksum experiment (Intel, csumdiag harness)

A single-ZMM 64 B/iter rolling checksum (~10 insns vs AVX2's 15/16) was written
and parity-verified (64 B–1 MB). Measured with the csumdiag time-boxed
harness on the Intel machine vs the AVX2 path:

| Block | AVX2 GB/s | AVX-512 GB/s |
|-------|:---------:|:------------:|
| 1 KB | 30.6 | 21.7 |
| 8 KB | 47.5 | 46.8 |
| 16 KB | 50.1 | 54.0 |
| 32 KB | 48.9 | 56.0 |
| 64 KB | 47.3 | 58.9 |
| 128 KB | 48.3 | 61.0 |
| 256 KB | 48.8 | 62.0 |
| 1 MB | 40.5 | 43.9 |

- **On Intel, AVX-512 wins from 16 KB up (+8–27%, peak +27% at 256 KB)** —
  Cascade Lake's full-width 512-bit integer SIMD (ZMM at the same throughput
  as YMM) makes the fewer-instruction ZMM loop pay off. 1 MB ties (memory
  bandwidth bound). Below 8 KB the ZMM fixed overhead loses.
- **On Zen 4 the same ZMM loop is *slower* everywhere** (1 KB −37% to
  256 KB −6%): AMD's 512-bit integer throughput is half-width plus
  AVX-512 frequency downclocking. This confirms AVX-512 rolling checksums
  stay off for AMD; the opt-in `Checksum1AVX512` is exposed for users who
  benchmark it faster on their own Intel server hardware.
- **⚠️ Not guaranteed on all Intel CPUs**: only measured on this one
  Cascade Lake Xeon (8269CY). Server Xeons (Skylake-X … Sapphire Rapids)
  share the 2×512-bit FMA design and should behave similarly, but client
  parts differ (10th/11th gen downclocks, 12th gen+ has no AVX-512 at all).
  Benchmark on your own hardware before enabling `Checksum1AVX512`.

## MD5 SIMD cores (zero-alloc)

> Benchmarks below live in the `hashsimd/` submodule (`go test -bench=... ./hashsimd/`).

### AMD Ryzen 9 8940HX

| Benchmark | Time | Throughput | B/op | allocs/op |
|-----------|------|-----------|------|-----------|
| MD5x8_Bulk (AVX2, 32 KB) | ~7.7 µs | 4.26 GB/s | 0 | 0 |
| MD5x8Core_Bulk (AVX2 raw) | ~81 µs | 6.31 GB/s | 0 | 0 |
| MD5x16Core_Bulk (AVX-512 raw) | ~91 µs | 11.24 GB/s | 0 | 0 |

### Intel Xeon 8269CY (reference only)

| Benchmark | Throughput |
|-----------|:----------:|
| `MD5x8_Bulk` (AVX2) | 2543 MB/s |
| `MD5x8Core_Bulk` (AVX2 raw) | 3891 MB/s |
| `MD5x16Core_Bulk` (AVX-512 raw) | 10575 MB/s |

**AVX-512 near-parity**: `MD5x16Core_Bulk` on Intel's full-width 512-bit
units is 10.58 GB/s vs 11.24 GB/s on Zen 4 — the big Intel gap on AVX2
integer SIMD nearly disappears on AVX-512.

## Rolling checksum vs rsync (AVX2, WSL2 Linux)

Measured in WSL2 Ubuntu (native Linux) on both machines. Both sides run their
own hand-written AVX2 rolling-checksum path: go-rsync `Checksum1` (64 B/iter,
`rolling_amd64.s`) and rsync 3.5.0dev `get_checksum1`
(`simd-checksum-x86_64.cpp` + `simd-checksum-avx2.S`, compiled with
`-O2 -mavx2 -DUSE_ROLL_SIMD -DUSE_ROLL_ASM`).

Method: time-boxed 500 ms window per round (inner loop of 1000 calls between
clock checks), 5 rounds per block size, median reported (min/max spread
< 1.5%). Two data patterns: deterministic formula `(i*37)^(i>>3)` and random
(seeded). Execution order was alternated. The go-rsync side was additionally
cross-checked with `go test -bench`.

### AMD Ryzen 9 8940HX (deterministic data)

> Re-measured 2026-08-03 in the same WSL session (alternating execution order,
> 500 ms time-box, 5 rounds, median). go-rsync is on the current code
> (16-bit-lane + conditional prefetch); rsync is the rebuilt SIMD tool.

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 78.0 | 49.0 |
| 2 KB | 94.3 | 62.3 |
| 4 KB | 92.8 | 67.0 |
| 8 KB | 105.9 | 76.7 |
| 16 KB | 110.9 | 79.7 |
| 32 KB | 111.9 | 80.6 |
| 64 KB | 109.8 | 81.2 |
| 128 KB | 111.3 | 81.7 |
| 256 KB | 112.9 | 81.8 |
| 1 MB | 99.5 | 79.6 |

### AMD Ryzen 9 8940HX (random data)

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 79.0 | 49.6 |
| 2 KB | 94.4 | 59.3 |
| 4 KB | 100.3 | 67.5 |
| 8 KB | 109.6 | 72.4 |
| 16 KB | 112.4 | 75.0 |
| 32 KB | 112.3 | 75.8 |
| 64 KB | 108.0 | 76.3 |
| 128 KB | 111.5 | 76.8 |
| 256 KB | 107.3 | 76.9 |
| 1 MB | 105.0 | 75.0 |

### Intel Xeon 8269CY (deterministic data, reference only)

> Same tooling and methodology as the AMD rows; measured on a fresh
> ebmc6.26xlarge instance (2026-08-03). **Reference only** (rented server).

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 31.6 | 21.1 |
| 2 KB | 36.4 | 20.4 |
| 4 KB | 45.6 | 32.2 |
| 8 KB | 47.5 | 33.8 |
| 16 KB | 50.0 | 35.3 |
| 32 KB | 49.0 | 35.9 |
| 64 KB | 47.3 | 36.4 |
| 128 KB | 48.3 | 35.6 |
| 256 KB | 48.8 | 35.7 |
| 1 MB | 42.6 | 32.8 |

### Intel Xeon 8269CY (random data, reference only)

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 31.5 | 24.7 |
| 2 KB | 39.8 | 20.8 |
| 4 KB | 45.3 | 32.2 |
| 8 KB | 47.5 | 33.8 |
| 16 KB | 50.0 | 35.3 |
| 32 KB | 48.8 | 35.9 |
| 64 KB | 47.3 | 36.4 |
| 128 KB | 48.3 | 35.3 |
| 256 KB | 48.8 | 35.4 |
| 1 MB | 38.3 | 33.2 |

### Observations (Intel vs AMD)

- **Absolute throughput is ~40% of the AMD machine** (go-rsync peak 50.0 vs
  112.9 GB/s; rsync 36.4 vs 81.8): 2.5–3.8 GHz vs 4.5–5.2 GHz clock, plus
  lower per-cycle AVX2 integer-SIMD throughput on Intel (Cascade Lake
  VPMADDUBSW 256-bit is ~1/cycle vs 2/cycle on Zen 4).
- **go-rsync still leads, but by less than on AMD**: ~30–42% at 8 KB+
  (1 MB ~15–30%, memory-bandwidth bound) vs ~35–51% on AMD. With
  4 VPMADDUBSW per iteration saturating Intel's narrower SIMD throughput,
  both sides hit the same throughput wall, which compresses go-rsync's
  dependency-chain advantage (the 16-bit-lane no-fold trick pays off more
  on AMD, where throughput is not the binding constraint).
- **The Intel 4 KB dip is fixed by the conditional-prefetch change**
  (2026-08-03): with `PREFETCHT0` skipped for blocks < 64 KB, go-rsync's
  4 KB reading rose 30.3 → 46.1 GB/s (+52%) and the 2–32 KB range gained
  16–50%; ≥ 64 KB keeps the prefetch and is also slightly faster than the
  all-prefetch build. rsync's 2 KB dip (20.5 GB/s) is its own OOB-prefetch
  artifact and remains (rsync prefetches unconditionally).

## Cross-platform observations (full suite)

- Everything single-threaded is ~1.9–2.6× slower on Intel (2.5 GHz clock +
  narrower integer SIMD): signature hashing, Search, ApplyDelta, wire encode.
- `SignatureParallel` and `SearchParallel` scale with GOMAXPROCS — Intel's
  104-thread numbers are not comparable to AMD's 32-thread ones.
- Raw Intel count=3 medians (newest ebmc6.26xlarge instance) archived in
  [benchmarks-intel-full.md](benchmarks-intel-full.md).

## Reproducing the vs-rsync comparison

The two tools are kept out of this repository: the rsync side links GPL code,
and the go-rsync side is a standalone Go module. To reproduce, create the two
files below and run them on the same machine (Linux or WSL2 recommended).

**Prerequisites:** Go ≥ 1.26, g++, and an rsync 3.5.x source tree
(`./configure --enable-roll-simd` must succeed so `config.h` exists).

**1. go-rsync side** — `main.go` in a module `csumdiag` whose `go.mod` has
`require github.com/henryborner/go-rsync v0.0.0` and
`replace github.com/henryborner/go-rsync => ../go-rsync`:

```go
package main

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"time"

	delta "github.com/henryborner/go-rsync"
)

func fillData(data []byte, mode int) {
	for i := range data {
		switch mode {
		case 0:
			data[i] = byte((i*37)^(i>>3)) // formula
		case 1:
			data[i] = byte(rand.Intn(256)) // random
		case 2:
			data[i] = 0 // zeros
		default:
			data[i] = byte(i) // increasing
		}
	}
}

func main() {
	mode := 0
	if len(os.Args) > 1 {
		mode, _ = strconv.Atoi(os.Args[1])
	}
	rand.Seed(12345)
	sizes := []int{1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 1048576}
	for _, sz := range sizes {
		data := make([]byte, sz)
		fillData(data, mode)
		gbps := make([]float64, 5)
		for r := 0; r < 5; r++ {
			delta.Checksum1(data) // warmup
			// Time-boxed: run a fixed 500ms window so every size gets the same,
			// sufficiently long measurement period (no iteration-count guessing).
			target := 500 * time.Millisecond
			start := time.Now()
			count := 0
			for {
				for i := 0; i < 1000; i++ {
					delta.Checksum1(data)
					count++
				}
				if time.Since(start) >= target {
					break
				}
			}
			elapsed := time.Since(start)
			gbps[r] = float64(sz) * float64(count) / elapsed.Seconds() / 1e9
		}
		sort.Float64s(gbps)
		fmt.Printf("go-rsync %6d B: med %8.1f  min %7.1f  max %7.1f GB/s (mode %d)\n",
			sz, gbps[2], gbps[0], gbps[4], mode)
	}
}
```

Build & run:

```bash
GOOS=linux GOARCH=amd64 go build -o csumdiag .
./csumdiag 0   # deterministic data
./csumdiag 1   # random data
```

**2. rsync side** — `bench.cpp` placed inside the rsync source tree. It
deliberately does NOT `#include "rsync.h"` (that header defines a `new` macro
which breaks every C++ standard-library header); types are declared manually:

```cpp
// rsync SIMD rolling checksum benchmark (get_checksum1)
#include <cstdint>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <algorithm>

typedef uint32_t uint32;
typedef int32_t int32;

// Defined in simd-checksum-x86_64.cpp when USE_ROLL_SIMD is set (C linkage).
extern "C" uint32 get_checksum1(char *buf1, int32 len);

static void fill_data(char *data, int sz, int mode) {
    for (int i = 0; i < sz; i++) {
        switch (mode) {
        case 0: data[i] = (char)((i * 37) ^ (i >> 3)); break; // formula
        case 1: data[i] = (char)((rand() % 256) - 128); break; // random
        case 2: data[i] = 0; break;                            // zeros
        default: data[i] = (char)i; break;                     // increasing
        }
    }
}

int main(int argc, char **argv) {
    int mode = argc > 1 ? atoi(argv[1]) : 0;
    srand(12345);
    int sizes[] = {1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 1048576};
    for (int k = 0; k < 10; k++) {
        int sz = sizes[k];
        char *data = (char *)malloc(sz);
        fill_data(data, sz, mode);
        double gbps[5];
        for (int r = 0; r < 5; r++) {
            volatile uint32 sink = 0;
            sink += get_checksum1(data, sz); // warmup
            // Time-boxed: fixed 500ms window per round (inner loop of 1000
            // calls between clock checks keeps check overhead negligible).
            auto start = std::chrono::steady_clock::now();
            long long count = 0;
            while (true) {
                for (int i = 0; i < 1000; i++) {
                    sink += get_checksum1(data, sz);
                    count++;
                }
                if (std::chrono::steady_clock::now() - start >= std::chrono::milliseconds(500))
                    break;
            }
            auto end = std::chrono::steady_clock::now();
            double secs = std::chrono::duration<double>(end - start).count();
            gbps[r] = (double)sz * count / secs / 1e9;
        }
        std::sort(gbps, gbps + 5);
        printf("rsync  %7d B: med %8.1f  min %7.1f  max %7.1f GB/s (mode %d)\n",
               sz, gbps[2], gbps[0], gbps[4], mode);
        free(data);
    }
    return 0;
}
```

Build & run (must use `USE_ROLL_ASM` so the hand-written AVX2 assembly is
linked — the intrinsics-only build is not the rsync fast path on AMD):

```bash
cd <rsync-src>
g++ -O2 -mavx2 -DHAVE_CONFIG_H -DUSE_ROLL_SIMD -DUSE_ROLL_ASM -I. \
    bench.cpp simd-checksum-x86_64.cpp simd-checksum-avx2.S \
    -o rsync_csum_bench_avx2
./rsync_csum_bench_avx2 0   # deterministic data
./rsync_csum_bench_avx2 1   # random data
```

**Methodology encoded in both tools:**

- **Time-boxed 500 ms window** per round (inner loop of 1000 calls between
  clock checks). Fixed iteration counts give small block sizes far too short
  windows and noise-dominated results — observed in practice (1 KB varied
  39–66 GB/s with a fixed iteration count, stable at ~65 GB/s with time-boxing).
- **5 rounds per block size**, median reported; min/max shown to expose spread
  (observed < 1.5%).
- **Data modes**: 0 = deterministic `(i*37)^(i>>3)`, 1 = random (seed 12345),
  2 = zeros, 3 = increasing. Run both modes; results were data-independent.
- **Alternate execution order** between the two tools to cancel CPU frequency
  drift; results were order-stable.
- The go-rsync side was cross-checked with `go test -bench`
  (`BenchmarkChecksum1`), giving consistent numbers.

## Reproduce (go test)

```bash
go test -bench='BenchmarkSignature$|BenchmarkSignatureXXH64$|BenchmarkSignatureXXH3$|BenchmarkSignatureSHA256$|BenchmarkSignatureParallel$|BenchmarkMD5x8_Bulk$|BenchmarkMD5x8Core_Bulk$|BenchmarkMD5x16Core_Bulk$|BenchmarkChecksum1$|BenchmarkSignatureReader$' -benchmem -count=3 .
```
