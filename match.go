package delta

import (
	"bytes"
	"hash"
	"io"
)

// BlockSum represents a single block checksum from file B.
// BlockSum 表示文件 B 中一个块的校验和。
type BlockSum struct {
	Index  int    // block index / 块索引
	Sum1   uint32 // weak rolling checksum / 弱滚动校验和
	Sum2   []byte // strong checksum (MD5/SHA256) / 强校验和
	Offset int64  // byte offset within the file / 块在文件中的偏移
	Length int32  // actual block length (last block may be shorter) / 块长（末块可能更短）
}

type MatchResult struct {
	IsLiteral bool   // true = literal data, false = block reference / true=字面量, false=块引用
	Data      []byte // literal payload / 字面量数据
	BlockIdx  int    // matched block index / 匹配的块索引
	Offset    int64  // source offset (for ordering) / 来源中的偏移（用于排序）
}

type Signature struct {
	BlockSize int32      // block size / 块大小
	BlockSums []BlockSum // all block checksums / 所有块的校验和
	FileSize  int64      // original file size / 文件原始大小
}

type hashEntry struct {
	sum1   uint32
	idx    int
	offset int64
	length int32
}

// computeTableSize returns hash table size with ~80% load factor.
// Same formula as rsync: count/8*10+11, minimum 65536.
func computeTableSize(blockCount int) uint32 {
	ts := uint32(blockCount/8)*10 + 11
	if ts < 65536 {
		ts = 65536
	}
	return ts
}

// CHUNK_SIZE is the maximum literal chunk size (same as rsync's 32KB).
// Large literals are split into CHUNK_SIZE pieces to ensure the receiver
// never allocates more than 32KB at once (safe for low-memory servers).
// CHUNK_SIZE 字面量分块上限，同 rsync 的 32KB。
// 大字面量拆分为多个 CHUNK_SIZE 块，确保接收端单次缓冲区分配不超过此值。
const CHUNK_SIZE = 32 * 1024

// MatchEngine is the delta match engine.
// MatchEngine 增量匹配引擎。
type MatchEngine struct {
	blockSize  int32
	strongHash func() hash.Hash // strong checksum factory / 强校验和工厂
	checksums  []BlockSum       // checksums from the receiver / 目标端发来的校验和列表
	hashTable  [][]hashEntry    // dynamic hash table / 动态大小哈希表
	tableSize  uint32           // current table size / 当前表大小

	// stats / 统计
	HashHits     int
	FalseAlarms  int
	Matches      int
	LiteralBytes int64
}

// NewMatchEngine creates a new match engine.
// NewMatchEngine 创建匹配引擎。
func NewMatchEngine(blockSize int32, strongAlgo string) *MatchEngine {
	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}
	return &MatchEngine{
		blockSize:  blockSize,
		strongHash: algo.New,
	}
}

func (me *MatchEngine) LoadSignature(sig *Signature) {
	me.checksums = sig.BlockSums
	me.buildHashTable()
}

func (me *MatchEngine) buildHashTable() {
	// Dynamic table size: ~80% load factor, same formula as rsync.
	// Odd size ensures modulo distributes across all buckets.
	ts := computeTableSize(len(me.checksums))
	me.tableSize = ts
	me.hashTable = make([][]hashEntry, ts)

	for i, cs := range me.checksums {
		var h uint32
		if ts == 65536 {
			// Traditional: (s1+s2) & 0xFFFF for 16-bit hash.
			// Using s1+s2 (like rsync's SUM2HASH2) gives much better
			// distribution than s1 alone.
			h = (cs.Sum1 + cs.Sum1>>16) & 0xFFFF
		} else {
			// Large table: full 32-bit sum modulo odd table size.
			// Odd divisor ensures high bits of sum2 contribute.
			h = cs.Sum1 % ts
		}
		me.hashTable[h] = append(me.hashTable[h], hashEntry{
			sum1:   cs.Sum1,
			idx:    i,
			offset: cs.Offset,
			length: cs.Length,
		})
	}
}

// Search searches source data for matches, returning an instruction sequence.
// Search 在源数据中搜索匹配，返回指令序列。
func (me *MatchEngine) Search(data []byte) []MatchResult {
	if len(me.checksums) == 0 || len(data) < int(me.blockSize) {
		return me.emitLiterals(nil, data, 0)
	}

	var results []MatchResult
	rs := NewRollingSum(data[:me.blockSize])
	offset := int64(0)
	lastMatch := int64(0)
	wantIdx := 0 // encourage adjacent matches / 鼓励相邻匹配

	for offset+int64(me.blockSize) <= int64(len(data)) {
		matched := false

		// Level 1: hash table lookup
		var h uint32
		v := rs.Value()
		if me.tableSize == 65536 {
			// Use exact same formula as buildHashTable
			h = (v + v>>16) & 0xFFFF
		} else {
			h = v % me.tableSize
		}
		bucket := me.hashTable[h]

		if len(bucket) > 0 {
			me.HashHits++

			// Cache strong sum per offset (same as rsync's done_csum2).
			// 同一 offset 只算一次强校验和（同 rsync done_csum2）。
			blockData := data[offset : offset+int64(me.blockSize)]
			computedSum2 := me.computeStrong(blockData)

			for _, entry := range bucket {
				if entry.sum1 != rs.Value() {
					continue
				}

				// Level 3: strong checksum verification (pre-computed, zero overhead).
				// Level 3: 强校验和验证（预计算，无重复开销）。
				if !bytes.Equal(computedSum2, me.checksums[entry.idx].Sum2) {
					me.FalseAlarms++
					continue
				}

				matchIdx := entry.idx
				if matchIdx != wantIdx && wantIdx < len(me.checksums) {
					wantEntry := me.checksums[wantIdx]
					if wantEntry.Sum1 == rs.Value() &&
						bytes.Equal(computedSum2, wantEntry.Sum2) {
						matchIdx = wantIdx
					}
				}
				wantIdx = matchIdx + 1

				if offset > lastMatch {
					results = me.emitLiterals(results, data[lastMatch:offset], lastMatch)
				}

				// emit block reference / 发送块引用
				results = append(results, MatchResult{
					IsLiteral: false,
					BlockIdx:  matchIdx,
					Offset:    offset,
				})

				me.Matches++
				lastMatch = offset + int64(me.blockSize)
				offset = lastMatch
				matched = true
				break
			}
		}

		if !matched {

			if offset+int64(me.blockSize) < int64(len(data)) {
				rs.Roll(data[offset], data[offset+int64(me.blockSize)], me.blockSize)
			}
			offset++
		} else if offset+int64(me.blockSize) <= int64(len(data)) {

			rs.Reset(data[offset : offset+int64(me.blockSize)])
		}
	}

	// remaining literal data / 剩余文字数据
	if lastMatch < int64(len(data)) {
		results = me.emitLiterals(results, data[lastMatch:], lastMatch)
	}

	return results
}

// emitLiterals splits literal data into ≤CHUNK_SIZE MatchResults,
// ensuring the receiver's single buffer allocation stays ≤ 32KB.
// emitLiterals 将字面量数据拆分为多个 ≤CHUNK_SIZE 的 MatchResult，
// 确保接收端单次缓冲区分配不超过 32KB（小内存服务器安全）。
func (me *MatchEngine) emitLiterals(results []MatchResult, data []byte, offset int64) []MatchResult {
	for len(data) > 0 {
		n := int32(len(data))
		if n > CHUNK_SIZE {
			n = CHUNK_SIZE
		}
		results = append(results, MatchResult{
			IsLiteral: true,
			Data:      data[:n],
			Offset:    offset,
		})
		me.LiteralBytes += int64(n)
		data = data[n:]
		offset += int64(n)
	}
	return results
}

// computeStrong computes the strong checksum for the given data.
// Used in the search loop to avoid repeated hash.New calls.
// computeStrong 计算给定数据的强校验和（用于搜索循环预计算，避免重复 hash.New）。
func (me *MatchEngine) computeStrong(data []byte) []byte {
	h := me.strongHash()
	h.Reset()
	h.Write(data)
	return h.Sum(nil)
}

// GenerateSignature generates block signatures from in-memory data.
// GenerateSignature 从内存数据生成块签名。
//
// This is a convenience wrapper around GenerateSignatureReader.
// For large files, prefer GenerateSignatureReader to stream from disk
// and avoid holding the entire file in memory.
func GenerateSignature(data []byte, blockSize int32, strongAlgo string) *Signature {
	return GenerateSignatureReader(bytes.NewReader(data), int64(len(data)), blockSize, strongAlgo)
}

// GenerateSignatureParallel generates block signatures from in-memory data.
// GenerateSignatureParallel 从内存数据并行生成块签名。
//
// Delegates to GenerateSignatureReader (AVX512 → AVX2 → scalar).
// For large files, prefer GenerateSignatureReader directly.
func GenerateSignatureParallel(data []byte, blockSize int32, strongAlgo string) *Signature {
	return GenerateSignature(data, blockSize, strongAlgo)
}

// GenerateSignatureReader generates block signatures from an io.Reader,
// avoiding loading the entire file into memory.
// Uses 8-way AVX2 acceleration for md5 when available.
// GenerateSignatureReader 从 io.Reader 流式生成块签名，避免全量读入内存。
// md5 + AVX512 可用时使用 16 路 SIMD；否则 AVX2 8 路；否则标量回退。
func GenerateSignatureReader(r io.Reader, fileSize int64, blockSize int32, strongAlgo string) *Signature {
	sig := &Signature{
		BlockSize: blockSize,
		FileSize:  fileSize,
	}

	numBlocks := (fileSize + int64(blockSize) - 1) / int64(blockSize)
	sig.BlockSums = make([]BlockSum, numBlocks)

	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}

	// Pre-allocate one contiguous buffer for all Sum2 slices.
	sumBuf := make([]byte, int(numBlocks)*algo.Length)

	// AVX512 16-way md5 fast path (blockSize >= 2KB only).
	// AVX512 gather overhead dominates for small blocks; threshold empirically
	// determined on Intel Xeon Platinum @ 2.5GHz (crossover at ~1400 bytes).
	if strongAlgo == "md5" && md5x16available() && blockSize >= 2048 {
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
					return sig
				}
				total += int(remain)
			}

			if n == batchSize {
				var off16, len16 [16]int
				var out16 [16][16]byte
				off := 0
				for b := 0; b < 16; b++ {
					off16[b] = off
					len16[b] = int(blockSize)
					off += int(blockSize)
				}
				md5Hash16wayAVX512(batchBuf, off16, len16, &out16)
				for b := 0; b < 16; b++ {
					idx := int(base) + b
					start := idx * algo.Length
					copy(sumBuf[start:], out16[b][:])
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: (base + int64(b)) * int64(blockSize),
						Length: blockSize,
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
		return sig
	}

	// AVX2 8-way md5 fast path: batch-read 8 blocks at a time for SIMD.
	if strongAlgo == "md5" && md5x8available() {
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
					return sig
				}
				total += int(remain)
			}

			if n == batchSize {
				// 8 full blocks → AVX2 SIMD
				var off8, len8 [8]int
				off := 0
				for b := 0; b < 8; b++ {
					off8[b] = off
					len8[b] = int(blockSize)
					off += int(blockSize)
				}
				var out8 [8][16]byte
				md5Hash8wayAVX2(batchBuf, off8, len8, &out8)
				for b := 0; b < 8; b++ {
					idx := int(base) + b
					start := idx * algo.Length
					copy(sumBuf[start:], out8[b][:])
					sig.BlockSums[idx] = BlockSum{
						Index:  idx,
						Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
						Sum2:   sumBuf[start : start+algo.Length],
						Offset: (base + int64(b)) * int64(blockSize),
						Length: blockSize,
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
		return sig
	}

	// Scalar path: non-md5 algorithms or platforms without AVX2.
	buf := make([]byte, blockSize)
	if algo.FastSum != nil {
		for i := int64(0); i < numBlocks; i++ {
			remain := fileSize - i*int64(blockSize)
			if remain > int64(blockSize) {
				remain = int64(blockSize)
			}
			if _, err := io.ReadFull(r, buf[:remain]); err != nil {
				break
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
				break
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

	return sig
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
