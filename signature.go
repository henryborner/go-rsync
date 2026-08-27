package delta

import (
	"bytes"
	"hash"
	"io"
	"runtime"
	"sync"

	"github.com/henryborner/go-rsync/hashsimd"
)

// GenerateSignature generates block signatures from in-memory data.
// GenerateSignature 从内存数据生成块签名。
//
// This is a convenience wrapper around GenerateSignatureReader.
// For large files, prefer GenerateSignatureReader to stream from disk
// and avoid holding the entire file in memory.
func GenerateSignature(data []byte, blockSize int32, strongAlgo string) *Signature {
	// bytes.NewReader never fails, so the error is unreachable.
	sig, _ := GenerateSignatureReader(bytes.NewReader(data), int64(len(data)), blockSize, strongAlgo)
	return sig
}

// GenerateSignatureParallel generates block signatures from in-memory data
// using multiple goroutines.  Blocks are distributed evenly across workers;
// each worker computes Checksum1 + strong hash independently.
//
// Uses the same SIMD-accelerated Checksum1 (AVX2/SSE2/pure-Go) and FastSum
// paths as the serial version.  Speedup is near-linear for large files since
// blocks are independent and data is read-only.
//
// GenerateSignatureParallel 使用多 goroutine 并行生成块签名。
// 块均匀分配给 worker，每个 worker 独立计算 Checksum1 + 强校验和。
// 复用 Checksum1 的 SIMD 加速路径。大文件加速比接近线性。
func GenerateSignatureParallel(data []byte, blockSize int32, strongAlgo string) (*Signature, error) {
	fileSize := int64(len(data))
	numBlocks := (fileSize + int64(blockSize) - 1) / int64(blockSize)
	if numBlocks <= 1 {
		return GenerateSignature(data, blockSize, strongAlgo), nil
	}

	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		return nil, err
	}

	sig := &Signature{
		BlockSize: blockSize,
		FileSize:  fileSize,
		BlockSums: make([]BlockSum, numBlocks),
	}
	sumBuf := make([]byte, int(numBlocks)*algo.Length)

	// Distribute work: each goroutine processes a contiguous range of blocks.
	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > int(numBlocks) {
		numWorkers = int(numBlocks)
	}
	blocksPerWorker := (int(numBlocks) + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		startBlock := w * blocksPerWorker
		endBlock := startBlock + blocksPerWorker
		if endBlock > int(numBlocks) {
			endBlock = int(numBlocks)
		}
		if startBlock >= endBlock {
			wg.Done()
			continue
		}

		go func(start, end int) {
			defer wg.Done()

			// Use SIMD batch path (8-way AVX2 / 4-way NEON) when available for md5.
			if strongAlgo == "md5" && hashsimd.MD5x8Available() && blockSize >= 64 {
				const batch = 8
				for base := start; base < end; base += batch {
					n := batch
					if base+n > end {
						n = end - base
					}
					dataOff := int64(base) * int64(blockSize)
					batchEnd := dataOff + int64(n)*int64(blockSize)
					if batchEnd > fileSize {
						batchEnd = fileSize
					}

					// Only use SIMD if all blocks in this batch are full-sized.
					batchBuf := data[dataOff:batchEnd]
					if n == batch && batchEnd-dataOff >= int64(batch)*int64(blockSize) {
						var off8, len8 [8]int
						o := 0
						for b := 0; b < 8; b++ {
							off8[b] = o
							len8[b] = int(blockSize)
							o += int(blockSize)
						}
						var out8 [8][16]byte
						hashsimd.MD5Hash8way(batchBuf, off8, len8, &out8)
						for b := 0; b < 8; b++ {
							idx := base + b
							sum2Start := idx * algo.Length
							copy(sumBuf[sum2Start:], out8[b][:])
							blkLen := int32(blockSize)
							if r := fileSize - int64(idx)*int64(blockSize); r < int64(blockSize) {
								blkLen = int32(r)
							}
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
								Sum2:   sumBuf[sum2Start : sum2Start+algo.Length],
								Offset: int64(idx) * int64(blockSize),
								Length: blkLen,
							}
						}
					} else {
						// Tail < 8 blocks → scalar.
						for b := 0; b < n; b++ {
							idx := base + b
							off := int64(idx) * int64(blockSize)
							remain := fileSize - off
							if remain > int64(blockSize) {
								remain = int64(blockSize)
							}
							block := data[off : off+remain]
							sum2Start := idx * algo.Length
							sum2 := algo.FastSum(sumBuf[sum2Start:sum2Start+algo.Length], block)
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(block),
								Sum2:   sum2,
								Offset: off,
								Length: int32(len(block)),
							}
						}
					}
				}
				return
			}

			if strongAlgo == "md5" && hashsimd.MD5x4Available() && blockSize >= 64 {
				const batch = 4
				for base := start; base < end; base += batch {
					n := batch
					if base+n > end {
						n = end - base
					}
					dataOff := int64(base) * int64(blockSize)
					batchEnd := dataOff + int64(n)*int64(blockSize)
					if batchEnd > fileSize {
						batchEnd = fileSize
					}

					batchBuf := data[dataOff:batchEnd]
					if n == batch && batchEnd-dataOff >= int64(batch)*int64(blockSize) {
						var off4, len4 [4]int
						o := 0
						for b := 0; b < 4; b++ {
							off4[b] = o
							len4[b] = int(blockSize)
							o += int(blockSize)
						}
						var out4 [4][16]byte
						hashsimd.MD5Hash4way(batchBuf, off4, len4, &out4)
						for b := 0; b < 4; b++ {
							idx := base + b
							sum2Start := idx * algo.Length
							copy(sumBuf[sum2Start:], out4[b][:])
							blkLen := int32(blockSize)
							if r := fileSize - int64(idx)*int64(blockSize); r < int64(blockSize) {
								blkLen = int32(r)
							}
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
								Sum2:   sumBuf[sum2Start : sum2Start+algo.Length],
								Offset: int64(idx) * int64(blockSize),
								Length: blkLen,
							}
						}
					} else {
						for b := 0; b < n; b++ {
							idx := base + b
							off := int64(idx) * int64(blockSize)
							remain := fileSize - off
							if remain > int64(blockSize) {
								remain = int64(blockSize)
							}
							block := data[off : off+remain]
							sum2Start := idx * algo.Length
							sum2 := algo.FastSum(sumBuf[sum2Start:sum2Start+algo.Length], block)
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(block),
								Sum2:   sum2,
								Offset: off,
								Length: int32(len(block)),
							}
						}
					}
				}
				return
			}

			// Scalar fallback: per-block Checksum1 + strong hash.
			hasFastSum := algo.FastSum != nil
			var h hash.Hash
			if !hasFastSum {
				h = algo.New()
			}
			for i := start; i < end; i++ {
				off := int64(i) * int64(blockSize)
				remain := fileSize - off
				if remain > int64(blockSize) {
					remain = int64(blockSize)
				}
				block := data[off : off+remain]

				sum2Start := i * algo.Length
				var sum2 []byte
				if hasFastSum {
					sum2 = algo.FastSum(sumBuf[sum2Start:sum2Start+algo.Length], block)
				} else {
					h.Reset()
					h.Write(block)
					sum2 = h.Sum(sumBuf[sum2Start : sum2Start : sum2Start+algo.Length])
				}

				sig.BlockSums[i] = BlockSum{
					Index:  i,
					Sum1:   Checksum1(block),
					Sum2:   sum2,
					Offset: off,
					Length: int32(len(block)),
				}
			}
		}(startBlock, endBlock)
	}

	wg.Wait()
	return sig, nil
}

// GenerateSignatureReader generates block signatures from an io.Reader,
// avoiding loading the entire file into memory.
// Uses 16-way AVX-512, 8-way AVX2, or 4-way NEON acceleration for md5 when available.
// GenerateSignatureReader 从 io.Reader 流式生成块签名，避免全量读入内存。
// md5 + AVX512 可用时使用 16 路 SIMD；否则 AVX2 8 路；否则标量回退。
func GenerateSignatureReader(r io.Reader, fileSize int64, blockSize int32, strongAlgo string) (*Signature, error) {
	sig := &Signature{
		BlockSize: blockSize,
		FileSize:  fileSize,
	}

	numBlocks := (fileSize + int64(blockSize) - 1) / int64(blockSize)
	sig.BlockSums = make([]BlockSum, numBlocks)

	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		return nil, err
	}

	// Pre-allocate one contiguous buffer for all Sum2 slices.
	sumBuf := make([]byte, int(numBlocks)*algo.Length)

	// AVX512 16-way md5 fast path (blockSize >= 2KB only).
	// AVX512 gather overhead dominates for small blocks; threshold empirically
	// determined on Intel Xeon Platinum @ 2.5GHz (crossover at ~1400 bytes).
	if strongAlgo == "md5" && hashsimd.MD5x16Available() && blockSize >= 2048 {
		const batchSize = 16
		batchBuf := make([]byte, batchSize*int(blockSize))

		for base := int64(0); base < numBlocks; base += batchSize {
			n := batchSize
			if base+int64(n) > numBlocks {
				n = int(numBlocks - base)
			}

			total := 0
			for b := 0; b < n; b++ {
				remain := fileSize - (base+int64(b))*int64(blockSize)
				if remain > int64(blockSize) {
					remain = int64(blockSize)
				}
				if _, err := io.ReadFull(r, batchBuf[total:total+int(remain)]); err != nil {
					return sig, err
				}
				total += int(remain)
			}

			// Only use SIMD if all blocks are exactly full-sized (total bytes match).
			if n == batchSize && total == batchSize*int(blockSize) {
				var off16, len16 [16]int
				var out16 [16][16]byte
				off := 0
				for b := 0; b < 16; b++ {
					off16[b] = off
					len16[b] = int(blockSize)
					off += int(blockSize)
				}
				hashsimd.MD5Hash16way(batchBuf, off16, len16, &out16)
				for b := 0; b < 16; b++ {
					idx := int(base) + b
					start := idx * algo.Length
					copy(sumBuf[start:], out16[b][:])
					blkLen := int32(blockSize)
					if r := fileSize - (base+int64(b))*int64(blockSize); r < int64(blockSize) {
						blkLen = int32(r)
					}
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: (base + int64(b)) * int64(blockSize),
						Length: blkLen,
					}
				}
			} else {
				off := 0
				for b := 0; b < n; b++ {
					idx := int(base) + b
					remain := fileSize - int64(idx)*int64(blockSize)
					if remain > int64(blockSize) {
						remain = int64(blockSize)
					}
					block := batchBuf[off : off+int(remain)]
					start := idx * algo.Length
					algo.FastSum(sumBuf[start:start+algo.Length], block)
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(block),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: int64(idx) * int64(blockSize),
						Length: int32(len(block)),
					}
					off += int(remain)
				}
			}
		}
		return sig, nil
	}

	// AVX2 8-way md5 fast path: batch-read 8 blocks at a time for SIMD.
	if strongAlgo == "md5" && hashsimd.MD5x8Available() {
		const batchSize = 8
		batchBuf := make([]byte, batchSize*int(blockSize))

		for base := int64(0); base < numBlocks; base += batchSize {
			n := batchSize
			if base+int64(n) > numBlocks {
				n = int(numBlocks - base)
			}

			// Read n blocks into batchBuf
			total := 0
			for b := 0; b < n; b++ {
				remain := fileSize - (base+int64(b))*int64(blockSize)
				if remain > int64(blockSize) {
					remain = int64(blockSize)
				}
				if _, err := io.ReadFull(r, batchBuf[total:total+int(remain)]); err != nil {
					return sig, err
				}
				total += int(remain)
			}

			// Only use SIMD if all blocks are exactly full-sized.
			if n == batchSize && total == batchSize*int(blockSize) {
				// 8 full blocks → AVX2 SIMD
				var off8, len8 [8]int
				off := 0
				for b := 0; b < 8; b++ {
					off8[b] = off
					len8[b] = int(blockSize)
					off += int(blockSize)
				}
				var out8 [8][16]byte
				hashsimd.MD5Hash8way(batchBuf, off8, len8, &out8)
				for b := 0; b < 8; b++ {
					idx := int(base) + b
					start := idx * algo.Length
					copy(sumBuf[start:], out8[b][:])
					blkLen := int32(blockSize)
					if r := fileSize - (base+int64(b))*int64(blockSize); r < int64(blockSize) {
						blkLen = int32(r)
					}
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: (base + int64(b)) * int64(blockSize),
						Length: blkLen,
					}
				}
			} else {
				// Tail < 8 blocks → scalar fallback
				off := 0
				for b := 0; b < n; b++ {
					idx := int(base) + b
					remain := fileSize - int64(idx)*int64(blockSize)
					if remain > int64(blockSize) {
						remain = int64(blockSize)
					}
					block := batchBuf[off : off+int(remain)]
					start := idx * algo.Length
					algo.FastSum(sumBuf[start:start+algo.Length], block)
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(block),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: int64(idx) * int64(blockSize),
						Length: int32(len(block)),
					}
					off += int(remain)
				}
			}
		}
		return sig, nil
	}

	// NEON 4-way md5 fast path for ARM64.
	if strongAlgo == "md5" && hashsimd.MD5x4Available() {
		const batchSize = 4
		batchBuf := make([]byte, batchSize*int(blockSize))

		for base := int64(0); base < numBlocks; base += batchSize {
			n := batchSize
			if base+int64(n) > numBlocks {
				n = int(numBlocks - base)
			}

			total := 0
			for b := 0; b < n; b++ {
				remain := fileSize - (base+int64(b))*int64(blockSize)
				if remain > int64(blockSize) {
					remain = int64(blockSize)
				}
				if _, err := io.ReadFull(r, batchBuf[total:total+int(remain)]); err != nil {
					return sig, err
				}
				total += int(remain)
			}

			if n == batchSize && total == batchSize*int(blockSize) {
				var off4, len4 [4]int
				off := 0
				for b := 0; b < 4; b++ {
					off4[b] = off
					len4[b] = int(blockSize)
					off += int(blockSize)
				}
				var out4 [4][16]byte
				hashsimd.MD5Hash4way(batchBuf, off4, len4, &out4)
				for b := 0; b < 4; b++ {
					idx := int(base) + b
					start := idx * algo.Length
					copy(sumBuf[start:], out4[b][:])
					blkLen := int32(blockSize)
					if r := fileSize - (base+int64(b))*int64(blockSize); r < int64(blockSize) {
						blkLen = int32(r)
					}
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: (base + int64(b)) * int64(blockSize),
						Length: blkLen,
					}
				}
			} else {
				off := 0
				for b := 0; b < n; b++ {
					idx := int(base) + b
					remain := fileSize - int64(idx)*int64(blockSize)
					if remain > int64(blockSize) {
						remain = int64(blockSize)
					}
					block := batchBuf[off : off+int(remain)]
					start := idx * algo.Length
					algo.FastSum(sumBuf[start:start+algo.Length], block)
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(block),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: int64(idx) * int64(blockSize),
						Length: int32(len(block)),
					}
					off += int(remain)
				}
			}
		}
		return sig, nil
	}

	// Scalar path: non-md5/sha256 algorithms or platforms without AVX2.
	buf := make([]byte, blockSize)
	if algo.FastSum != nil {
		for i := int64(0); i < numBlocks; i++ {
			remain := fileSize - i*int64(blockSize)
			if remain > int64(blockSize) {
				remain = int64(blockSize)
			}
			if _, err := io.ReadFull(r, buf[:remain]); err != nil {
				return sig, err
			}
			block := buf[:remain]
			start := int(i) * algo.Length

			sig.BlockSums[i] = BlockSum{
				Index:  int(i),
				Sum1:   Checksum1(block),
				Sum2:   algo.FastSum(sumBuf[start:start+algo.Length], block),
				Offset: i * int64(blockSize),
				Length: int32(len(block)),
			}
		}
	} else {
		h := algo.New() // reuse single hash instance
		for i := int64(0); i < numBlocks; i++ {
			remain := fileSize - i*int64(blockSize)
			if remain > int64(blockSize) {
				remain = int64(blockSize)
			}
			if _, err := io.ReadFull(r, buf[:remain]); err != nil {
				return sig, err
			}
			block := buf[:remain]

			h.Reset()
			h.Write(block)
			start := int(i) * algo.Length
			sum2 := h.Sum(sumBuf[start : start : start+algo.Length])

			sig.BlockSums[i] = BlockSum{
				Index:  int(i),
				Sum1:   Checksum1(block),
				Sum2:   sum2,
				Offset: i * int64(blockSize),
				Length: int32(len(block)),
			}
		}
	}

	return sig, nil
}

func CalculateBlockSize(fileSize int64) int32 {
	switch {
	case fileSize < 1:
		return 700
	case fileSize <= 490*1024: // <= 490KB
		return 700
	default:
		bs := int32(fileSize / 10000)
		if bs < 700 {
			bs = 700
		}
		if bs > 128*1024 {
			bs = 128 * 1024
		}
		return bs
	}
}
