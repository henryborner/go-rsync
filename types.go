package delta

import "hash"

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
