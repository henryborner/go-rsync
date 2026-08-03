# go-rsync Benchmarks

> Complete benchmark results with per-operation allocation counts. Measured on
> an **AMD Ryzen 9 8940HX (Zen 4)** running Windows 11, Go 1.26.5. Each value
> is the median of 3 runs (`-count=3 -benchmem`).

## Hardware

| | |
|---|---|
| CPU | AMD Ryzen 9 8940HX (Zen 4, 16 cores / 32 threads) |
| L3 cache | 64 MB (full) |
| OS / Go | Windows 11 / go1.26.5 |

## GenerateSignature (1 MB data, single-threaded, blockSize=700)

| Algorithm | Path | Time | Throughput | B/op | allocs/op |
|-----------|------|------|-----------|------|-----------|
| md5 | AVX2 8-way (`md5x8_amd64.s`) | ~341 µs | 2.93 GB/s | 120,928 | 5 |
| sha256 | stdlib SHA-NI (`crypto/sha256`) | ~616 µs | 1.62 GB/s | 140,064 | 5 |
| xxh64 | cespare/xxhash | ~151 µs | 6.62 GB/s | 103,200 | 5 |
| xxh3 | zeebo/xxh3 | ~116 µs | 8.62 GB/s | 115,488 | 5 |

## GenerateSignatureParallel (md5)

| Data | blockSize | Path | Time | Throughput | B/op | allocs/op |
|------|-----------|------|------|-----------|------|-----------|
| 1 MB | 700 | AVX2 8-way | ~129 µs | 8.1 GB/s | 120,386 | 68 |
| 10 MB | 1048 | AVX2 8-way | ~389 µs | 26.9 GB/s | 734,784 | 68 |
| 100 MB | 10485 | AVX2 8-way | ~2.37 ms | 44.3 GB/s | 734,794 | 68 |

This path has **no AVX-512 16-way branch** — md5 stays on 8-way AVX2 at every
block size; the 44.3 GB/s comes from 32-way parallelism, not AVX-512.

## SignatureReader (streaming, md5)

| Config | Path | Time | Throughput | B/op | allocs/op |
|--------|------|------|-----------|------|-----------|
| 10MB_700B | AVX2 8-way | ~3.30 ms | 3.18 GB/s | 1,095,776 | 5 |
| 10MB_32KB | AVX-512 16-way | ~3.22 ms | 3.26 GB/s | 548,192 | 5 |
| 10MB_128KB | AVX-512 16-way | ~3.30 ms | 3.18 GB/s | 2,103,392 | 5 |
| 100MB_700B | AVX2 8-way | ~33.4 ms | 3.15 GB/s | 10,803,296 | 5 |
| 100MB_128KB | AVX-512 16-way | ~32.3 ms | 3.25 GB/s | 2,159,968 | 5 |

md5 dispatch: blockSize ≥ 2 KB → AVX-512 16-way, otherwise AVX2 8-way.

## Search / delta matching (md5)

### Search (1 MB, blockSize=700)

| Benchmark | Data | Time | B/op | allocs/op |
|-----------|------|------|------|-----------|
| `Search` (90% match) | 1 MB, every 10th byte flipped | ~3.2 ms | — | — |
| `SearchMiss` (all-miss) | 1 MB, unrelated data | ~3.1 ms | — | — |
| `SearchReader` (streaming) | 1 MB | ~3.8 ms | 655,680 | 7 |

### SearchMatrix (size × match density)

| Data | miss | match90 | identical |
|------|------|---------|-----------|
| 1 MB (blockSize 700) | 330 MB/s | 330 MB/s | 903 MB/s |
| 32 MB (blockSize ~2 KB) | 210 MB/s | 210 MB/s | 1066 MB/s |

- miss ≈ match90: flipping 10% of bytes breaks ~20% of blocks, so the damaged
  regions dominate and are scanned byte-by-byte in both cases.
- identical jumps by blockSize on every match (~0.9–1.1 GB/s).
- Large files drop to ~210 MB/s (data + table leave L2; TLB pressure).

### SearchParallel (1 MB, md5)

| Workers | Time |
|---------|------|
| 1 | 3.1 ms |
| 2 | 1.8 ms |
| 4 | 1.2 ms |
| 8 | 1.0 ms |

## Delta pipeline (reconstruct / roundtrip)

| Benchmark | Time (1 MB) | Throughput |
|-----------|-------------|-----------|
| `ApplyDelta` Match90 (~90% block refs) | ~371 µs | ~2.8 GB/s |
| `ApplyDelta` AllLiteral (all literals) | ~355 µs | ~3.0 GB/s |
| `RoundTrip` (Delta + ApplyDelta + verify) | ~4.19 ms | ~250 MB/s (search-dominated) |

## RollingSum hot path (Roll + Value)

| Benchmark | ns/op |
|-----------|-------|
| `RollValue`/RollOnly | ~1.84 ns |
| `RollValue`/RollAndValue | ~2.07 ns |

~10 cycles/byte — the serial dependency floor of the search hot path; the
hash-table lookup latency (L2/L3) dominates the remaining cost per byte.

## Wire format

| Stream | Encode | Decode |
|--------|--------|--------|
| Signature (1 MB, ~1500 blocks) | ~1.03 GB/s | ~656 MB/s |
| Instructions (typical 10%-modified delta) | ~5.5 GB/s | ~8.2 GB/s |

## Checksum1 (rolling weak checksum — zero-alloc)

| Size | Time | Throughput | B/op | allocs/op |
|------|------|-----------|------|-----------|
| 1 KB | ~12.7 ns | 80.8 GB/s | 0 | 0 |
| 8 KB | ~74.8 ns | 109.5 GB/s | 0 | 0 |
| 64 KB | ~588 ns | 111.4 GB/s | 0 | 0 |
| 1 MB | ~9.73 µs | 107.7 GB/s | 0 | 0 |

(v7 16-bit-lane rewrite, 2026-08-02: 19→16 instructions, no VPMADDWD. Pre-v7:
64.2 / 79.0 / 80.2 / 80.4 GB/s.)

## Rolling checksum vs rsync (AVX2, same machine, WSL2 Linux)

Measured in WSL2 Ubuntu (native Linux) on the same AMD Ryzen 9 8940HX.
Both sides run their own hand-written AVX2 rolling-checksum path:
go-rsync `Checksum1` (64 B/iter, `rolling_amd64.s`) and rsync 3.5.0dev
`get_checksum1` (`simd-checksum-x86_64.cpp` + `simd-checksum-avx2.S`,
compiled with `-O2 -mavx2 -DUSE_ROLL_SIMD -DUSE_ROLL_ASM`).

Method: time-boxed 500 ms window per round (inner loop of 1000 calls between
clock checks), 5 rounds per block size, median reported (min/max spread
< 1.5%). Two data patterns: deterministic formula `(i*37)^(i>>3)` and random
(seeded). Execution order was alternated. The go-rsync side was additionally
cross-checked with `go test -bench`.

### Deterministic data

> Both columns re-measured 2026-08-02 in the same WSL session (alternating
> execution order, 500 ms time-box, 5 rounds, median). go-rsync is on the
> current v7 (16-bit-lane) code; rsync is the rebuilt SIMD tool.

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 78.4 | 53.2 |
| 2 KB | 92.4 | 63.4 |
| 4 KB | 104.4 | 72.4 |
| 8 KB | 109.4 | 77.8 |
| 16 KB | 111.8 | 80.6 |
| 32 KB | 112.0 | 81.5 |
| 64 KB | 111.7 | 82.0 |
| 128 KB | 112.5 | 82.6 |
| 256 KB | 113.6 | 82.6 |
| 1 MB | 106.6 | 80.7 |

### Random data

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 79.4 | 52.7 |
| 2 KB | 94.1 | 63.5 |
| 4 KB | 103.5 | 72.2 |
| 8 KB | 106.2 | 77.7 |
| 16 KB | 109.0 | 80.4 |
| 32 KB | 107.8 | 81.5 |
| 64 KB | 110.5 | 82.1 |
| 128 KB | 112.4 | 82.6 |
| 256 KB | 113.4 | 82.6 |
| 1 MB | 104.6 | 80.6 |

### Intel Xeon 8269CY (Aliyun bare-metal) — reference only

> Measured 2026-08-03 on an **Intel Xeon Platinum 8269CY bare-metal server**
> (Aliyun, 2 sockets × 26 cores = 52 cores / 104 threads, 2.5 GHz base /
> 3.8 GHz max, Cascade Lake generation, full 71.5 MiB L3 per socket).
> Same tooling and methodology as the AMD rows (time-boxed 500 ms, 5 rounds,
> median, alternating execution order). **Reference only**: unlike the AMD
> numbers (measured on the local machine), this is a rented Aliyun server —
> CPU frequency, Turbo behavior, and virtualization/neighbor conditions are
> not under our control and may drift between sessions.

#### Deterministic data

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 32.8 | 24.2 |
| 2 KB | 39.1 | 20.5 |
| 4 KB | 30.3 | 32.2 |
| 8 KB | 36.0 | 33.8 |
| 16 KB | 40.5 | 35.2 |
| 32 KB | 43.2 | 36.0 |
| 64 KB | 44.7 | 36.5 |
| 128 KB | 45.6 | 35.7 |
| 256 KB | 46.1 | 35.8 |
| 1 MB | 41.9 | 32.2 |

#### Random data

| Block size | go-rsync GB/s | rsync AVX2 GB/s |
|-----------|--------------|-----------------|
| 1 KB | 31.8 | 23.6 |
| 2 KB | 38.7 | 20.4 |
| 4 KB | 30.3 | 32.4 |
| 8 KB | 36.4 | 33.9 |
| 16 KB | 40.5 | 35.4 |
| 32 KB | 43.2 | 35.9 |
| 64 KB | 44.7 | 36.4 |
| 128 KB | 45.6 | 35.3 |
| 256 KB | 46.1 | 35.4 |
| 1 MB | 42.1 | 31.8 |

#### Observations (Intel vs AMD)

- **Absolute throughput is ~40% of the AMD machine** (go-rsync peak 46.1 vs
  113.6 GB/s; rsync 36.5 vs 82.6): 2.5–3.8 GHz vs 4.5–5.2 GHz clock, plus
  lower per-cycle AVX2 integer-SIMD throughput on Intel (Cascade Lake
  VPMADDUBSW 256-bit is ~1/cycle vs 2/cycle on Zen 4).
- **go-rsync still leads, but by less**: ~7–30% at 8 KB+ (peak +26% at
  256 KB) vs 38–53% on AMD. With 4 VPMADDUBSW per iteration saturating
  Intel's narrower SIMD throughput, both sides hit the same throughput wall,
  which compresses go-rsync's dependency-chain advantage (the 16-bit-lane
  no-fold trick pays off more on AMD, where throughput is not the binding
  constraint).
- **Intel-only anomalies** (not present on AMD): go-rsync dips to 30.3 GB/s
  at 4 KB (below its 2 KB reading of 39.1) and rsync dips to 20.5 GB/s at
  2 KB (below its 1 KB reading of 23.6). The two dips are stable
  (min = max across runs, not noise) and offset between the two tools —
  suspected prefetch crossing the buffer end (both sides prefetch 384 B
  ahead) or Turbo behavior; under investigation.

### Reproducing this comparison

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
- **SHA-256 dispatch**: `GenerateSignature` uses the stdlib SHA-NI hardware
  path (`crypto/sha256`) whenever the CPU supports SHA-NI. The project's 8-way
  AVX2 SHA-256 core (`sha256x8_amd64.s`) is deliberately disabled in that case
  (`sha256x8available()` = AVX2 && !SHA-NI) because stdlib is faster. The
  `sha256` row above therefore measures **SHA-NI**, not the 8-way AVX2 core —
  which has no benchmark here because this machine (Zen 4) has SHA-NI.
- **MD5 dispatch**: blockSize ≥ 2 KB → AVX-512 16-way (`md5x16_amd64.s`);
  smaller blocks → AVX2 8-way (`md5x8_amd64.s`). `GenerateSignatureParallel`
  has no AVX-512 branch and stays on 8-way AVX2 at every block size.
- ARM64 NEON results (Checksum1 UDOT/VUMULL, 4-way MD5) are measured on ARM64
  CI (ubuntu-24.04-arm); see [neon-checksum.md](neon-checksum.md).

## Reproduce

```bash
go test -bench='BenchmarkSignature$|BenchmarkSignatureXXH64$|BenchmarkSignatureXXH3$|BenchmarkSignatureSHA256$|BenchmarkMD5x8_Bulk$|BenchmarkMD5x8Core_Bulk$|BenchmarkMD5x16Core_Bulk$|BenchmarkChecksum1$|BenchmarkSignatureReader$' -benchmem -count=3 .
```
