package delta

import (
	"bytes"
	"hash"
	"io"
	"runtime"
	"sync"
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
// Standard hash table sizing: count/8*10+11, minimum 65536.
func computeTableSize(blockCount int) uint32 {
	ts := uint32(blockCount/8)*10 + 11
	if ts < 65536 {
		ts = 65536
	}
	return ts
}

// CHUNK_SIZE is the maximum literal chunk size (32KB).
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
	cachedHash hash.Hash        // reused across Search to avoid per-hit allocation
	cachedSum  []byte           // reused sum buffer to avoid Sum(nil) allocation

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
		cachedHash: algo.New(),
	}
}

func (me *MatchEngine) LoadSignature(sig *Signature) {
	me.checksums = sig.BlockSums
	me.buildHashTable()
}

func (me *MatchEngine) buildHashTable() {
	// Dynamic table size: ~80% load factor.
	// Odd size ensures modulo distributes across all buckets.
	ts := computeTableSize(len(me.checksums))
	me.tableSize = ts
	me.hashTable = make([][]hashEntry, ts)

	for i, cs := range me.checksums {
		var h uint32
		if ts == 65536 {
			// Traditional: (s1+s2) & 0xFFFF for 16-bit hash.
			// Using the full Sum1 value (s1 + s2 packed) gives much better
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

			// Lazy strong sum: only compute MD5 when sum1 matches.
			// For large files, 99%+ of hash hits fail at sum1 comparison.
			// Computing MD5 before sum1 check wastes ~16TB of hashing on a 1GB file.
			var sum2Done bool
			var computedSum2 []byte

			for _, entry := range bucket {
				if entry.sum1 != rs.Value() {
					continue
				}

				// Only compute strong checksum when weak sum matches.
				if !sum2Done {
					blockData := data[offset : offset+int64(me.blockSize)]
					computedSum2 = me.computeStrong(blockData)
					sum2Done = true
				}

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

	// remaining literal data — but first check if trailing bytes match
	// a partial last block from the signature.
	// 剩余文字数据 — 先检查尾部是否匹配签名中的末不完整块。
	if lastMatch < int64(len(data)) {
		tail := data[lastMatch:]
		// Try to match tail against the last block of each signature block.
		// The last block's offset = blockIdx * blockSize.
		for i := len(me.checksums) - 1; i >= 0; i-- {
			bs := me.checksums[i]
			blockStart := int64(bs.Index) * int64(me.blockSize)
			if blockStart != lastMatch {
				continue
			}
			if int64(bs.Length) != int64(len(tail)) {
				continue
			}
			if Checksum1(tail) != bs.Sum1 {
				continue
			}
			if !bytes.Equal(me.computeStrong(tail), bs.Sum2) {
				continue
			}
			// Matched! Emit block reference instead of literal.
			results = append(results, MatchResult{
				IsLiteral: false,
				BlockIdx:  bs.Index,
				Offset:    lastMatch,
			})
			me.Matches++
			lastMatch += int64(bs.Length)
			break
		}
	}

	// Emit any remaining unmatched bytes as literal.
	if lastMatch < int64(len(data)) {
		results = me.emitLiterals(results, data[lastMatch:], lastMatch)
	}

	return results
}

// SearchReader performs streaming delta matching from an io.Reader.
// Results are delivered via fn callback as they are discovered.
// MatchResult.Data points into internal buffers and is only valid during fn.
//
// Memory usage is O(blockSize), independent of file size: at most
// 2×blockSize + CHUNK_SIZE bytes of buffered data.  Suitable for
// low-memory servers and multi-GB files piped over SSH.
//
// SearchReader 从 io.Reader 流式执行增量匹配。
// 每发现一条匹配/字面量指令就回调 fn，数据仅回调期间有效。
//
// 内存占用 O(blockSize)，与文件大小无关：最多缓冲 2×blockSize + CHUNK_SIZE 字节。
// 适合内存受限的服务器和 SSH 管道传输超大文件。
func (me *MatchEngine) SearchReader(r io.Reader, fileSize int64, fn func(MatchResult) error) error {
	// Small file or no checksums: stream all as literals.
	if len(me.checksums) == 0 || fileSize < int64(me.blockSize) {
		return me.streamLiterals(r, fileSize, fn)
	}

	// Fixed-size buffer: literal backlog + window + lookahead.
	// Flush literals when backlog reaches CHUNK_SIZE to keep memory bounded.
	bufCap := 2*int(me.blockSize) + CHUNK_SIZE
	if bufCap < 4*CHUNK_SIZE {
		bufCap = 4 * CHUNK_SIZE // tiny blockSize guard
	}
	buf := make([]byte, bufCap)
	bufBase := int64(0) // file offset of buf[0]
	bufLen := 0         // valid bytes in buf

	// Read initial window + lookahead.
	need := int64(me.blockSize) * 2
	if need > fileSize {
		need = fileSize
	}
	if err := me.readInto(r, buf, &bufLen, &bufBase, fileSize, need); err != nil {
		if bufLen == 0 {
			return err
		}
	}
	if bufLen < int(me.blockSize) {
		return fn(MatchResult{IsLiteral: true, Data: buf[:bufLen], Offset: 0})
	}

	rs := NewRollingSum(buf[:me.blockSize])
	offset := int64(0)       // current window start (absolute file offset)
	literalStart := int64(0) // first byte not yet emitted (absolute)
	wantIdx := 0
	needReset := false // true when rolling sum needs reset after a match

	// Pre-compute constants to avoid repeated conversions in the hot loop.
	blockSize64 := int64(me.blockSize)
	chunkSize64 := int64(CHUNK_SIZE)
	bufEnd := bufBase + int64(bufLen) // current end of buffered data

	// Deferred check thresholds: only re-check buffer/literal every blockSize bytes.
	nextBufCheck := offset + blockSize64
	nextLiteralCheck := literalStart + chunkSize64

	for offset+blockSize64 <= fileSize {
		// ── Periodic buffer boundary check (~every blockSize iterations) ──
		if offset >= nextBufCheck {
			// Ensure we have 2×blockSize bytes ahead for rolling safety.
			needEnd := offset + 2*blockSize64
			if needEnd > fileSize {
				needEnd = fileSize
			}
			if bufEnd < needEnd {
				if err := me.shiftAndFill(r, buf, &bufLen, &bufBase, literalStart, fileSize, needEnd); err != nil {
					_ = me.flushRemaining(fn, buf, bufBase, bufLen, &literalStart, fileSize)
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						return nil
					}
					return err
				}
				bufEnd = bufBase + int64(bufLen)
				if offset+blockSize64 > bufEnd {
					_ = me.flushRemaining(fn, buf, bufBase, bufLen, &literalStart, fileSize)
					return nil
				}
			}
			nextBufCheck = offset + blockSize64
		}

		// Reset rolling sum after a match (data is now guaranteed available).
		if needReset {
			offIdx := int(offset - bufBase)
			rs.Reset(buf[offIdx : offIdx+int(blockSize64)])
			needReset = false
			nextBufCheck = offset + blockSize64
		}

		matched := false

		// ── Hash table lookup (same logic as Search) ──
		var h uint32
		v := rs.Value()
		if me.tableSize == 65536 {
			h = (v + v>>16) & 0xFFFF
		} else {
			h = v % me.tableSize
		}
		bucket := me.hashTable[h]

		if len(bucket) > 0 {
			me.HashHits++
			var sum2Done bool
			var computedSum2 []byte
			offIdx := int(offset - bufBase)

			for _, entry := range bucket {
				if entry.sum1 != rs.Value() {
					continue
				}

				if !sum2Done {
					blockData := buf[offIdx : offIdx+int(blockSize64)]
					computedSum2 = me.computeStrong(blockData)
					sum2Done = true
				}

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

				// Emit pending literals before this match.
				if offset > literalStart {
					if err := me.emitLiteralChunks(fn, buf[literalStart-bufBase:offIdx], literalStart); err != nil {
						return err
					}
				}

				// Emit match instruction.
				me.Matches++
				if err := fn(MatchResult{IsLiteral: false, BlockIdx: matchIdx, Offset: offset}); err != nil {
					return err
				}

				literalStart = offset + blockSize64
				offset = literalStart
				nextLiteralCheck = literalStart + chunkSize64
				matched = true
				needReset = true
				break
			}
		}

		if !matched {
			// Periodic literal backlog flush (~every CHUNK_SIZE iterations).
			if offset >= nextLiteralCheck {
				flushEnd := literalStart + chunkSize64
				if err := me.emitLiteralChunks(fn, buf[literalStart-bufBase:flushEnd-bufBase], literalStart); err != nil {
					return err
				}
				literalStart = flushEnd
				nextLiteralCheck = literalStart + chunkSize64
			}

			offIdx := int(offset - bufBase)
			if offset+blockSize64 < fileSize {
				nextOff := offIdx + int(blockSize64)
				rs.Roll(buf[offIdx], buf[nextOff], me.blockSize)
			}
			offset++
		}
	}

	// Try to match trailing bytes against a partial last block from the signature.
	// 检查尾部未匹配字节是否对应签名中的末不完整块。
	if literalStart < fileSize {
		tailLen := fileSize - literalStart
		for i := len(me.checksums) - 1; i >= 0; i-- {
			bs := me.checksums[i]
			blockStart := int64(bs.Index) * int64(me.blockSize)
			if blockStart != literalStart || int64(bs.Length) != tailLen {
				continue
			}
			// Ensure buffer covers the tail.
			if bufBase+int64(bufLen) < literalStart+tailLen {
				break
			}
			tail := buf[literalStart-bufBase : literalStart-bufBase+tailLen]
			if Checksum1(tail) != bs.Sum1 {
				continue
			}
			if !bytes.Equal(me.computeStrong(tail), bs.Sum2) {
				continue
			}
			// Matched — emit as block reference.
			me.Matches++
			if err := fn(MatchResult{IsLiteral: false, BlockIdx: bs.Index, Offset: literalStart}); err != nil {
				return err
			}
			literalStart += tailLen
			break
		}
	}

	// Emit trailing unmatched bytes.
	return me.flushRemaining(fn, buf, bufBase, bufLen, &literalStart, fileSize)
}

// ── Buffer helpers for SearchReader ──

// readInto reads from r into buf until buf covers at least needEnd bytes
// (absolute file offset), or EOF/error.  Updates bufLen and bufBase.
func (me *MatchEngine) readInto(r io.Reader, buf []byte, bufLen *int, bufBase *int64,
	fileSize int64, needEnd int64) error {
	for *bufBase+int64(*bufLen) < needEnd && *bufBase+int64(*bufLen) < fileSize {
		if *bufLen >= len(buf) {
			return io.ErrShortBuffer
		}
		n, err := r.Read(buf[*bufLen:])
		if n > 0 {
			*bufLen += n
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// shiftAndFill discards buffered data before keepFrom, shifts remaining data
// to buf[0], then reads more to satisfy needEnd.  Updates bufLen and bufBase.
func (me *MatchEngine) shiftAndFill(r io.Reader, buf []byte, bufLen *int, bufBase *int64,
	keepFrom int64, fileSize int64, needEnd int64) error {
	// Discard prefix: copy [keepFrom..] to buf[0].
	if keepFrom > *bufBase {
		shift := int(keepFrom - *bufBase)
		if shift < *bufLen {
			copy(buf, buf[shift:*bufLen])
			*bufLen -= shift
		} else {
			*bufLen = 0
		}
		*bufBase = keepFrom
	}
	return me.readInto(r, buf, bufLen, bufBase, fileSize, needEnd)
}

// emitLiteralChunks emits data as ≤CHUNK_SIZE literal MatchResults via fn.
func (me *MatchEngine) emitLiteralChunks(fn func(MatchResult) error, data []byte, fileOffset int64) error {
	for len(data) > 0 {
		n := int32(len(data))
		if n > CHUNK_SIZE {
			n = CHUNK_SIZE
		}
		if err := fn(MatchResult{IsLiteral: true, Data: data[:n], Offset: fileOffset}); err != nil {
			return err
		}
		me.LiteralBytes += int64(n)
		data = data[n:]
		fileOffset += int64(n)
	}
	return nil
}

// flushRemaining emits all remaining buffered data from literalStart to fileSize
// as literal chunks.  Used at EOF / end of search.
func (me *MatchEngine) flushRemaining(fn func(MatchResult) error, buf []byte, bufBase int64,
	bufLen int, literalStart *int64, fileSize int64) error {
	if *literalStart >= fileSize {
		return nil
	}
	// Read any unread tail.
	// (We can't call shiftAndFill here without an io.Reader, so we work
	// with whatever is already buffered.  The caller handles EOF.)
	avail := bufBase + int64(bufLen) - *literalStart
	if avail <= 0 {
		return nil
	}
	remaining := fileSize - *literalStart
	if avail > remaining {
		avail = remaining
	}
	if avail > 0 {
		data := buf[*literalStart-bufBase : *literalStart-bufBase+avail]
		if err := me.emitLiteralChunks(fn, data, *literalStart); err != nil {
			return err
		}
		*literalStart += avail
	}
	return nil
}

// streamLiterals reads the entire reader content and emits as literal chunks.
func (me *MatchEngine) streamLiterals(r io.Reader, fileSize int64, fn func(MatchResult) error) error {
	buf := make([]byte, CHUNK_SIZE)
	var offset int64
	for offset < fileSize {
		n := int64(CHUNK_SIZE)
		if offset+n > fileSize {
			n = fileSize - offset
		}
		rn, err := io.ReadFull(r, buf[:n])
		if rn > 0 {
			if cbErr := fn(MatchResult{IsLiteral: true, Data: buf[:rn], Offset: offset}); cbErr != nil {
				return cbErr
			}
			me.LiteralBytes += int64(rn)
			offset += int64(rn)
		}
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
	}
	return nil
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
// Uses a cached hash instance and sum buffer to avoid per-call allocation.
func (me *MatchEngine) computeStrong(data []byte) []byte {
	me.cachedHash.Reset()
	me.cachedHash.Write(data)
	// Reuse sum buffer — Sum appends to the provided slice, no allocation.
	me.cachedSum = me.cachedHash.Sum(me.cachedSum[:0])
	return me.cachedSum
}

// ── Parallel search ─────────────────────────────────────────────────────

// SearchParallel splits data into numWorkers overlapping segments and runs
// Search on each in parallel.  The hash table and checksum list are shared
// read-only across workers; each worker gets its own strong-hash instance.
//
// Results are merged in file order.  For identical files, speedup is
// ~5-7× on 8-core machines.
//
// SearchParallel 将数据拆分为 numWorkers 个重叠段，并行运行 Search。
// 哈希表和校验和列表跨 worker 只读共享。结果按文件顺序合并。
//
// If numWorkers ≤ 1 or data is too small to split, falls back to Search.
func (me *MatchEngine) SearchParallel(data []byte, numWorkers int) []MatchResult {
	fileSize := int64(len(data))
	if len(me.checksums) == 0 || fileSize < int64(me.blockSize) {
		return me.emitLiterals(nil, data, 0)
	}
	if numWorkers <= 1 {
		return me.Search(data)
	}

	blockSize64 := int64(me.blockSize)
	chunkSize := (fileSize + int64(numWorkers) - 1) / int64(numWorkers)
	// Round chunkSize up to next multiple of blockSize so segment
	// boundaries never split a block in the middle.
	chunkSize = ((chunkSize + blockSize64 - 1) / blockSize64) * blockSize64
	if chunkSize <= blockSize64 {
		return me.Search(data) // too small to split meaningfully
	}

	// Adjust worker count: don't create workers for empty chunks past EOF.
	actualWorkers := int((fileSize + chunkSize - 1) / chunkSize)
	if actualWorkers < numWorkers {
		numWorkers = actualWorkers
	}

	type segStats struct {
		hashHits, falseAlarms, matches int
		literalBytes                   int64
	}
	type segResult struct {
		segID   int
		results []MatchResult
		stats   segStats
	}
	ch := make(chan segResult, numWorkers)

	for i := 0; i < numWorkers; i++ {
		segStart := int64(i) * chunkSize
		segEnd := segStart + chunkSize
		if segEnd > fileSize {
			segEnd = fileSize
		}
		// Data window: segment bytes + blockSize of overlap for rolling.
		dataEnd := segEnd + blockSize64
		if dataEnd > fileSize {
			dataEnd = fileSize
		}
		segData := data[segStart:dataEnd]
		segLen := segEnd - segStart // bytes this segment is responsible for

		go func(segID int, segBytes []byte, startOff int64, maxOff int64) {
			w := me.fork()
			results := w.Search(segBytes)

			// Filter: keep results within [startOff, startOff+maxOff).
			// Matches starting before maxOff are always kept.
			// Literals extending beyond maxOff are trimmed.
			var filtered []MatchResult
			segEnd := startOff + maxOff
			for _, r := range results {
				r.Offset += startOff // make absolute

				if r.IsLiteral {
					litEnd := r.Offset + int64(len(r.Data))
					// Literal entirely before segment? Skip.
					if litEnd <= startOff {
						continue
					}
					// Literal starts beyond segment end? Skip.
					if r.Offset >= segEnd {
						continue
					}
					// Trim literal that crosses segment boundary.
					if litEnd > segEnd {
						trimTo := segEnd - r.Offset
						if trimTo <= 0 {
							continue
						}
						r.Data = r.Data[:trimTo]
					}
				} else {
					// Match: keep if it starts within the segment's window range.
					// Matches always cover blockSize bytes into the basis file;
					// their Offset is just for ordering.
					if r.Offset >= segEnd {
						continue
					}
				}
				filtered = append(filtered, r)
			}

			ch <- segResult{
				segID:   segID,
				results: filtered,
				stats: segStats{
					hashHits:     w.HashHits,
					falseAlarms:  w.FalseAlarms,
					matches:      w.Matches,
					literalBytes: w.LiteralBytes,
				},
			}
		}(i, segData, segStart, segLen)
	}

	// Collect and merge results in segment order.
	segResults := make([][]MatchResult, numWorkers)
	for i := 0; i < numWorkers; i++ {
		sr := <-ch
		segResults[sr.segID] = sr.results
		// Aggregate stats (single goroutine, no race).
		me.HashHits += sr.stats.hashHits
		me.FalseAlarms += sr.stats.falseAlarms
		me.Matches += sr.stats.matches
		me.LiteralBytes += sr.stats.literalBytes
	}

	// Flatten in order.
	var total int
	for _, r := range segResults {
		total += len(r)
	}
	all := make([]MatchResult, 0, total)
	for _, r := range segResults {
		all = append(all, r...)
	}
	return all
}

// fork creates a lightweight copy of the MatchEngine that shares the
// read-only hash table and checksum list.  The copy gets its own strong-hash
// instance (cachedHash) for thread safety.
func (me *MatchEngine) fork() *MatchEngine {
	return &MatchEngine{
		blockSize:  me.blockSize,
		strongHash: me.strongHash,
		checksums:  me.checksums, // shared, read-only
		hashTable:  me.hashTable, // shared, read-only
		tableSize:  me.tableSize,
		cachedHash: me.strongHash(), // fresh instance per worker
	}
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
func GenerateSignatureParallel(data []byte, blockSize int32, strongAlgo string) *Signature {
	fileSize := int64(len(data))
	numBlocks := (fileSize + int64(blockSize) - 1) / int64(blockSize)
	if numBlocks <= 1 {
		return GenerateSignature(data, blockSize, strongAlgo)
	}

	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
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

			// Use SIMD batch path (8-way) when available for md5/sha256.
			if strongAlgo == "md5" && md5x8available() && blockSize >= 64 {
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
						md5Hash8wayAVX2(batchBuf, off8, len8, &out8)
						for b := 0; b < 8; b++ {
							idx := base + b
							sum2Start := idx * algo.Length
							copy(sumBuf[sum2Start:], out8[b][:])
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
								Sum2:   sumBuf[sum2Start : sum2Start+algo.Length],
								Offset: int64(idx) * int64(blockSize),
								Length: blockSize,
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

			if strongAlgo == "sha256" && sha256x8available() && blockSize >= 64 {
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
					batchBuf := data[dataOff:batchEnd]

					if n == batch && batchEnd-dataOff >= int64(batch)*int64(blockSize) {
						var off8, len8 [8]int
						o := 0
						for b := 0; b < 8; b++ {
							off8[b] = o
							len8[b] = int(blockSize)
							o += int(blockSize)
						}
						var out8 [8][32]byte
						sha256Hash8wayAVX2(batchBuf, off8, len8, &out8)
						for b := 0; b < 8; b++ {
							idx := base + b
							sum2Start := idx * algo.Length
							copy(sumBuf[sum2Start:], out8[b][:])
							sig.BlockSums[idx] = BlockSum{
								Index:  idx,
								Sum1:   Checksum1(batchBuf[b*int(blockSize) : (b+1)*int(blockSize)]),
								Sum2:   sumBuf[sum2Start : sum2Start+algo.Length],
								Offset: int64(idx) * int64(blockSize),
								Length: blockSize,
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
	return sig
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

	// AVX2 8-way sha256 fast path
	if strongAlgo == "sha256" && sha256x8available() {
		const batchSize = 8
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
				var off8, len8 [8]int
				off := 0
				for b := 0; b < 8; b++ {
					off8[b] = off
					len8[b] = int(blockSize)
					off += int(blockSize)
				}
				var out8 [8][32]byte
				sha256Hash8wayAVX2(batchBuf, off8, len8, &out8)
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

	// Scalar path: non-md5/sha256 algorithms or platforms without AVX2.
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
