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
	sum1 uint32 // packed weak checksum (s1 | s2<<16), each 16-bit / 打包弱校验和（s1|s2<<16，各 16 位）
	idx  int32  // block index + 1 (0 = empty slot) / 块索引 +1（0 表示空槽）
}

// CHUNK_SIZE is the maximum literal chunk size (32KB).
// Large literals are split into CHUNK_SIZE pieces to ensure the receiver
// never allocates more than 32KB at once (safe for low-memory servers).
// CHUNK_SIZE 字面量分块上限（32KB）。
// 大字面量拆分为多个 CHUNK_SIZE 块，确保接收端单次缓冲区分配不超过此值。
const CHUNK_SIZE = 32 * 1024

// maxChainLen caps the number of same-weak-checksum candidates compared
// at a single file offset.  Without this cap, a signature with thousands
// of blocks sharing one weak checksum (common in disk images with large
// runs of identical blocks) turns the inner loop into an O(file_size ×
// chain_length) scan.  The skipped data is sent literally — always correct,
// only slightly affecting compression ratio.
//
// Note (open-addressing rewrite): maxProbeLen (32) bounds the whole probe
// chain *before* sum1-matched candidates are ever compared, so chainLen can
// never reach maxChainLen.  It is kept as a defensive ceiling in case
// maxProbeLen is ever raised above it.  The effective per-offset cap is now
// maxProbeLen (32) — more conservative than the old 1024 (may give up
// sooner on pathological same-block clusters, lowering compression ratio
// slightly but never correctness).
// 注意（开放寻址重写后）：maxProbeLen(32) 在 sum1 匹配候选比较之前就先
// 截断整个探测链，因此 chainLen 永远到不了 maxChainLen。保留它是作为
// 未来 maxProbeLen 上调时的防御性上限。当前实际生效的上限是 maxProbeLen
// (32)——比旧的 1024 更保守（病态同块聚集时可能更早放弃，轻微降低压缩率，
// 但绝不影响正确性）。
const maxChainLen = 1024

// maxProbeLen caps the open-addressing probe chain length at a single file
// offset.  Unlike the old bucketed table (where an empty bucket ends the
// scan immediately), a linear-probe miss walks every occupied slot until the
// next empty one.  With ≤50% load a normal miss needs ~2 probes, but a
// pathological signature where thousands of blocks share one hash value
// would otherwise make one offset scan the whole cluster.  Probing beyond
// this bound is treated as a miss — the data is sent literally, always
// correct, only slightly affecting compression ratio (same contract as
// maxChainLen).
// 开放寻址探测链上限。旧链式表空桶立即结束，线性探测则要走到下一个空槽；
// 正常负载（≤50%）miss 只需 ~2 次探测，但大量块共享同一哈希值的病态签名
// 会让单个偏移扫过整个聚集区。超过此上限视为 miss——数据按字面量发送，
// 始终正确，仅轻微影响压缩率（与 maxChainLen 同一契约）。
const maxProbeLen = 32

// MatchEngine is the delta match engine.
// MatchEngine 增量匹配引擎。
type MatchEngine struct {
	blockSize  int32
	strongHash func() hash.Hash // strong checksum factory / 强校验和工厂
	checksums  []BlockSum       // checksums from the receiver / 目标端发来的校验和列表
	hashTable  []hashEntry      // flat open-addressing table / 扁平开放寻址哈希表
	tableMask  uint32           // tableSize-1 (power of two) / 表大小掩码（2 的幂-1）
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
func NewMatchEngine(blockSize int32, strongAlgo string) (*MatchEngine, error) {
	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		return nil, err
	}
	return &MatchEngine{
		blockSize:  blockSize,
		strongHash: algo.New,
		cachedHash: algo.New(),
	}, nil
}

func (me *MatchEngine) LoadSignature(sig *Signature) {
	if sig == nil {
		me.checksums = nil
		return
	}
	me.checksums = sig.BlockSums
	me.buildHashTable()
}

func (me *MatchEngine) buildHashTable() {
	// Flat open addressing with linear probing.
	// Capacity: next power of two ≥ 2×blockCount, minimum 65536.
	//  - ≥2×blocks → ≤50% load keeps probe chains short.
	//  - floor of 65536 keeps the hash 16-bit and the table sparse for small
	//    files — sparse tables make the "empty slot" branch predictable in
	//    the all-miss hot path (dense 12-bit tables cost ~0.6 mispredicts/byte).
	//  - Signatures with >8K blocks (large files) get a 128K table even when
	//    2×blocks would fit in 64K: the 64K table becomes cache-competitive
	//    with the data stream and measurably slower on the miss hot path.
	// Power-of-two size lets us hash with (v+v>>16) & mask — no runtime division.
	// 扁平开放寻址 + 线性探测。容量 = ≥2×块数 的下一个 2 的幂，下限 65536：
	//  - 负载 ≤50% 保证探测链短；
	//  - 下限 65536 保持 16 位哈希且小文件表稀疏——miss 热路径的"空槽"分支可预测。
	//  - 大文件（块数 >8K）使用 128K 表：64K 表与数据流缓存竞争后 miss 明显变慢。
	cap := uint32(len(me.checksums)) * 2
	if cap < 16 {
		cap = 16
	}
	cap--
	cap |= cap >> 1
	cap |= cap >> 2
	cap |= cap >> 4
	cap |= cap >> 8
	cap |= cap >> 16
	cap++
	if cap < 65536 {
		cap = 65536
	}
	if cap == 65536 && len(me.checksums) > 8192 {
		cap = 131072
	}
	me.tableSize = cap
	me.tableMask = cap - 1
	me.hashTable = make([]hashEntry, cap)

	for i, cs := range me.checksums {
		// (s1+s2) & mask mixes both 16-bit components; identical in both
		// build and search.  Linear probe to the next empty slot.
		h := (cs.Sum1 + cs.Sum1>>16) & me.tableMask
		for {
			if me.hashTable[h].idx == 0 { // empty slot / 空槽
				me.hashTable[h].sum1 = cs.Sum1
				me.hashTable[h].idx = int32(i) + 1
				break
			}
			h = (h + 1) & me.tableMask
		}
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

		// Level 1: hash table lookup — flat open addressing, linear probing.
		// Probe chain replaces the old bucket list; empty slot ends the chain.
		// 扁平开放寻址 + 线性探测；探测链取代旧桶列表，空槽即链尾。
		v := rs.Value()
		h := (v + v>>16) & me.tableMask
		hit := false
		probeLen := 0
		var sum2Done bool
		var computedSum2 []byte
		chainLen := 0

		for {
			// Bound the probe chain (see maxProbeLen) — a pathological cluster
			// must not turn one offset into a full-table scan.
			probeLen++
			if probeLen > maxProbeLen {
				break
			}
			e := &me.hashTable[h]
			if e.idx == 0 { // empty slot → end of probe chain / 空槽=探测链结束
				break
			}
			if !hit {
				me.HashHits++
				hit = true
			}
			idx := int(e.idx) - 1
			if e.sum1 == v {
				// Cap per-offset work to prevent O(N²) on pathological
				// signatures (many blocks sharing the same weak checksum).
				chainLen++
				if chainLen > maxChainLen {
					break
				}

				// Lazy strong sum: only compute MD5 when sum1 matches.
				// For large files, 99%+ of hash hits fail at sum1 comparison.
				// Computing MD5 before sum1 check wastes ~16TB of hashing on a 1GB file.
				if !sum2Done {
					blockData := data[offset : offset+int64(me.blockSize)]
					computedSum2 = me.computeStrong(blockData)
					sum2Done = true
				}

				if !bytes.Equal(computedSum2, me.checksums[idx].Sum2) {
					me.FalseAlarms++
					h = (h + 1) & me.tableMask
					continue
				}

				matchIdx := idx
				if matchIdx != wantIdx && wantIdx < len(me.checksums) {
					wantEntry := me.checksums[wantIdx]
					if wantEntry.Sum1 == v &&
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
			h = (h + 1) & me.tableMask
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
// max(2×blockSize + CHUNK_SIZE, 4×CHUNK_SIZE) bytes of buffered data (the
// 4×CHUNK_SIZE floor guards tiny block sizes).  Suitable for
// low-memory servers and multi-GB files piped over SSH.
//
// SearchReader 从 io.Reader 流式执行增量匹配。
// 每发现一条匹配/字面量指令就回调 fn，数据仅回调期间有效。
//
// 内存占用 O(blockSize)，与文件大小无关：最多缓冲
// max(2×blockSize + CHUNK_SIZE, 4×CHUNK_SIZE) 字节（4×CHUNK_SIZE 下限保护小块大小）。
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
		// Partial data available: only suppress EOF/UnexpectedEOF; real I/O errors must propagate.
		if err != io.EOF && err != io.ErrUnexpectedEOF {
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
					if flushErr := me.flushRemaining(fn, buf, bufBase, bufLen, &literalStart, fileSize); flushErr != nil {
						return flushErr
					}
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						return nil
					}
					return err
				}
				bufEnd = bufBase + int64(bufLen)
				if offset+blockSize64 > bufEnd {
					return me.flushRemaining(fn, buf, bufBase, bufLen, &literalStart, fileSize)
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
		v := rs.Value()
		h := (v + v>>16) & me.tableMask
		hit := false
		probeLen := 0
		var sum2Done bool
		var computedSum2 []byte
		offIdx := int(offset - bufBase)
		chainLen := 0

		for {
			// Bound the probe chain (see maxProbeLen) — a pathological cluster
			// must not turn one offset into a full-table scan.
			probeLen++
			if probeLen > maxProbeLen {
				break
			}
			e := &me.hashTable[h]
			if e.idx == 0 { // empty slot → end of probe chain / 空槽=探测链结束
				break
			}
			if !hit {
				me.HashHits++
				hit = true
			}
			idx := int(e.idx) - 1
			if e.sum1 == v {
				chainLen++
				if chainLen > maxChainLen {
					break
				}

				if !sum2Done {
					blockData := buf[offIdx : offIdx+int(blockSize64)]
					computedSum2 = me.computeStrong(blockData)
					sum2Done = true
				}

				if !bytes.Equal(computedSum2, me.checksums[idx].Sum2) {
					me.FalseAlarms++
					h = (h + 1) & me.tableMask
					continue
				}

				matchIdx := idx
				if matchIdx != wantIdx && wantIdx < len(me.checksums) {
					wantEntry := me.checksums[wantIdx]
					if wantEntry.Sum1 == v &&
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
			h = (h + 1) & me.tableMask
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
			// Literals extending beyond maxOff are trimmed. A match that
			// starts inside this segment but extends past the boundary is
			// emitted as a literal up to the boundary instead of a block
			// reference; the next segment owns the bytes after the boundary.
			// This keeps segments independently decodable and prevents the
			// next worker from duplicating bytes already covered by a
			// cross-boundary match.
			var filtered []MatchResult
			segEnd := startOff + maxOff
			for _, r := range results {
				relOff := r.Offset
				r.Offset += startOff // make absolute

				if r.IsLiteral {
					litEnd := r.Offset + int64(len(r.Data))
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
					// Match: keep only if it fits before the segment end.
					if r.Offset >= segEnd {
						continue
					}
					blockLen := int64(me.blockSize)
					if r.BlockIdx >= 0 && r.BlockIdx < len(me.checksums) {
						blockLen = int64(me.checksums[r.BlockIdx].Length)
					}
					if r.Offset+blockLen > segEnd {
						// Cross-boundary match: turn the in-segment prefix
						// into literal data so the next segment starts clean.
						relEnd := int(maxOff)
						if relEnd > len(segBytes) {
							relEnd = len(segBytes)
						}
						if relEnd > int(relOff) {
							filtered = append(filtered, MatchResult{
								IsLiteral: true,
								Data:      segBytes[relOff:relEnd],
								Offset:    r.Offset,
							})
						}
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
		// Aggregate diagnostic counters (single goroutine, no race).
		// Matches/LiteralBytes are computed below from the final filtered
		// instruction stream so repeated SearchParallel calls accumulate the
		// same way Search does.
		me.HashHits += sr.stats.hashHits
		me.FalseAlarms += sr.stats.falseAlarms
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

	// Accumulate match/literal counters from the final filtered results.
	// Worker counters are measured on the pre-filter windows (including
	// overlap and cross-boundary matches), so they can differ from the
	// instructions that are actually returned. HashHits/FalseAlarms remain
	// diagnostic sums.
	for _, r := range all {
		if r.IsLiteral {
			me.LiteralBytes += int64(len(r.Data))
		} else {
			me.Matches++
		}
	}
	return all
}

// SearchReaderParallel streams newFile from r and matches it in fixed-size
// in-memory windows with SearchParallel. Results are delivered in file order
// via fn; as with SearchReader, literal Data is only valid during the
// callback and is overwritten by the next window.
//
// Memory is O(windowSize + per-window result slices), independent of
// fileSize. A match that would cross a window boundary is not found (the
// window does not read past the boundary); both windows still reconstruct
// byte-for-byte correctly and simply send that prefix as literals. For
// 32-64MiB windows this costs at most a block or two of compression per
// boundary, which is negligible in practice.
//
// SearchReaderParallel 以固定大小的内存窗口流式读取 newFile，并在每个窗口
// 内用 SearchParallel 并行匹配。结果按文件顺序通过 fn 回调；与 SearchReader
// 相同，literal Data 仅在回调期间有效，下一窗口会覆盖缓冲。
//
// 内存占用为 O(windowSize + 每窗口结果切片)，与文件总大小无关。跨窗口边界
// 的匹配不会被上一窗口发现，因此该前缀按 literal 发送；重建结果仍逐字节
// 正确。对 32-64MiB 窗口，每个边界最多损失一两个块的压缩率，实际可忽略。
func (me *MatchEngine) SearchReaderParallel(r io.Reader, fileSize int64, windowSize, workers int, fn func(MatchResult) error) error {
	if windowSize <= 0 {
		return me.SearchReader(r, fileSize, fn)
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	buf := make([]byte, windowSize)
	var base int64
	for remaining := fileSize; remaining > 0; {
		want := windowSize
		if int64(want) > remaining {
			want = int(remaining)
		}
		n, err := io.ReadFull(r, buf[:want])
		if n > 0 {
			results := me.SearchParallel(buf[:n], workers)
			for i := range results {
				results[i].Offset += base
			}
			for _, mr := range results {
				if err := fn(mr); err != nil {
					return err
				}
			}
			base += int64(n)
			remaining -= int64(n)
		}
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
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
		tableMask:  me.tableMask,
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

			if strongAlgo == "md5" && md5x4available() && blockSize >= 64 {
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
						md5Hash4wayNEON(batchBuf, off4, len4, &out4)
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
				md5Hash16wayAVX512(batchBuf, off16, len16, &out16)
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
				md5Hash8wayAVX2(batchBuf, off8, len8, &out8)
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
	if strongAlgo == "md5" && md5x4available() {
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
				md5Hash4wayNEON(batchBuf, off4, len4, &out4)
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
