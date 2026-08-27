package delta

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
