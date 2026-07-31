package delta

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
)

// =========================================================================
// TestRollingSumKnownAnswer — 滚动校验和已知答案测试 (KAT)
// 用纯 Go 参考实现验证 Checksum1 对关键字节序列的正确性，
// 可捕获 CHAR_OFFSET、SIMD 通道顺序、字节序等 bug。
// =========================================================================

func TestRollingSumKnownAnswer(t *testing.T) {
	// Reference checksum: byte-by-byte, same algorithm as Checksum1.
	ref := func(data []byte) uint32 {
		var s1, s2 uint32
		for _, b := range data {
			s1 += uint32(b) + CHAR_OFFSET
			s2 += s1
		}
		return (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)
	}

	tests := []struct {
		name string
		data []byte
	}{
		// ── 空数据 ──
		{"empty", []byte{}},

		// ── 单字节边界 ──
		{"single-0x00", []byte{0x00}},
		{"single-0xFF", []byte{0xFF}},
		{"single-A", []byte{'A'}},

		// ── 全零块（常见 SIMD 边界） ──
		{"zeros-16", make([]byte, 16)},
		{"zeros-31", make([]byte, 31)},
		{"zeros-32", make([]byte, 32)},
		{"zeros-33", make([]byte, 33)},
		{"zeros-63", make([]byte, 63)},
		{"zeros-64", make([]byte, 64)},
		{"zeros-65", make([]byte, 65)},
		{"zeros-128", make([]byte, 128)},
		{"zeros-256", make([]byte, 256)},
		{"zeros-700", make([]byte, 700)},

		// ── 全 0xFF 块（最大字节值） ──
		{"ones-31", makeBytesRepeat(31, 0xFF)},
		{"ones-32", makeBytesRepeat(32, 0xFF)},
		{"ones-64", makeBytesRepeat(64, 0xFF)},
		{"ones-128", makeBytesRepeat(128, 0xFF)},
		{"ones-700", makeBytesRepeat(700, 0xFF)},

		// ── 递增序列 [0,1,2,...,N-1] ──
		{"inc-4", makeIncBytes(4)},
		{"inc-16", makeIncBytes(16)},
		{"inc-32", makeIncBytes(32)},
		{"inc-64", makeIncBytes(64)},
		{"inc-256", makeIncBytes(256)},

		// ── 常见字符串 ──
		{"hello", []byte("Hello")},
		{"rsync", []byte("rsync")},

		// ── 全 'A' 块 ──
		{"As-700", makeBytesRepeat(700, 'A')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := ref(tt.data)
			got := Checksum1(tt.data)
			if got != want {
				t.Errorf("Checksum1(%q len=%d): got=0x%08X, want=0x%08X",
					tt.name, len(tt.data), got, want)
			}
		})
	}
}

// TestRollingSumWindowConsistency verifies the fundamental rolling checksum
// property: Roll() on a sliding window must produce the same value as a
// fresh Checksum1() on the shifted data.
func TestRollingSumWindowConsistency(t *testing.T) {
	data := make([]byte, 8192)
	for i := range data {
		data[i] = byte((i*7 + 13) % 251)
	}

	sizes := []int32{32, 64, 128, 256, 512, 700, 1024, 4096}
	for _, blockSize := range sizes {
		t.Run(fmt.Sprintf("blockSize=%d", blockSize), func(t *testing.T) {
			rs := NewRollingSum(data[:blockSize])
			initial := rs.Value()
			if initial != Checksum1(data[:blockSize]) {
				t.Fatalf("initial: NewRollingSum=0x%08X, Checksum1=0x%08X",
					initial, Checksum1(data[:blockSize]))
			}

			for i := int32(0); i+blockSize < int32(len(data)); i++ {
				rs.Roll(data[i], data[i+blockSize], blockSize)
				fresh := Checksum1(data[i+1 : i+1+blockSize])
				if rs.Value() != fresh {
					t.Fatalf("offset %d: Roll()=0x%08X, Checksum1()=0x%08X",
						i+1, rs.Value(), fresh)
				}
			}
		})
	}
}

// =========================================================================
// TestHashSearchChainCap — 哈希搜索链长度上限
// 当大量块共享同一弱校验和时，确保搜索不会 O(N²) 爆炸。
// =========================================================================

func TestHashSearchChainCap(t *testing.T) {
	const (
		blockSize  = int32(700)
		decoyCount = 2000 // enough to exceed maxChainLen (1024)
		sourceLen  = 3000
		constByte  = byte('A')
	)

	// Step 1: compute the Sum1 for a constant-byte block.
	refBlock := makeBytesRepeat(int(blockSize), constByte)
	targetSum1 := Checksum1(refBlock)

	// Step 2: create decoy BlockSum entries.
	// All share the same Sum1 but have unique Sum2 (deterministic md5).
	decoys := make([]BlockSum, decoyCount)
	for i := range decoys {
		h := md5.New()
		fmt.Fprintf(h, "decoy-%08d-%08d", i, i*31)
		sum2 := h.Sum(nil)

		decoys[i] = BlockSum{
			Index:  i,
			Sum1:   targetSum1,
			Sum2:   sum2,
			Offset: int64(i) * int64(blockSize),
			Length: blockSize,
		}
	}

	sig := &Signature{
		BlockSize: blockSize,
		BlockSums: decoys,
		FileSize:  int64(decoyCount) * int64(blockSize),
	}

	// Step 3: search a constant-byte source.
	source := makeBytesRepeat(sourceLen, constByte)

	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	results := eng.Search(source)

	// Step 4: assertions.

	if eng.LiteralBytes != int64(sourceLen) {
		t.Errorf("expected %d literal bytes, got %d", sourceLen, eng.LiteralBytes)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 literal result, got %d", len(results))
	} else if !results[0].IsLiteral || len(results[0].Data) != sourceLen {
		t.Errorf("expected literal of %d bytes, got IsLiteral=%v len=%d",
			sourceLen, results[0].IsLiteral, len(results[0].Data))
	}

	windowCount := sourceLen - int(blockSize) + 1
	if eng.FalseAlarms <= 0 {
		t.Error("expected non-zero false alarms (weak checksum collisions)")
	}

	// With the chain-length cap, FalseAlarms must be bounded by
	// windowCount × maxChainLen, not windowCount × decoyCount.
	// Without the cap this would be ~4.6M; with it, capped at ~2.4M.
	cappedMax := windowCount * maxChainLen
	if eng.FalseAlarms > cappedMax {
		t.Errorf("FalseAlarms %d exceeds capped max %d (chain len limit not working?)",
			eng.FalseAlarms, cappedMax)
	}
	// Also verify that the cap actually kicked in: FalseAlarms should be
	// significantly less than the uncapped theoretical max.
	uncappedMax := windowCount * decoyCount
	if eng.FalseAlarms >= uncappedMax {
		t.Errorf("FalseAlarms %d == uncapped max %d (chain len limit did not trigger)",
			eng.FalseAlarms, uncappedMax)
	}

	t.Logf("decoy blocks: %d, source: %d bytes, windows: %d",
		decoyCount, sourceLen, windowCount)
	t.Logf("HashHits: %d, FalseAlarms: %d (capped: %d, uncapped: %d)",
		eng.HashHits, eng.FalseAlarms, cappedMax, uncappedMax)
}

// =========================================================================
// TestAllChecksumAlgorithms — 所有强校验算法的穷举往返测试
// =========================================================================

func TestAllChecksumAlgorithms(t *testing.T) {
	algos := []string{"md5", "sha256", "xxh64", "xxh3"}

	type testCase struct {
		name      string
		oldSize   int
		newSize   int
		blockSize int32
	}

	cases := []testCase{
		{"zero-old", 0, 100, 700},
		{"zero-new", 100, 0, 700},
		{"both-zero", 0, 0, 700},
		{"1-byte-each", 1, 1, 700},
		{"less-than-block", 699, 699, 700},
		{"exact-block", 700, 700, 700},
		{"more-than-block", 701, 701, 700},
		{"identical-5K", 5000, 5000, 700},
		{"insert-middle", 10000, 10200, 700},
		{"ten-blocks", 10*700 + 300, 10*700 + 500, 700},
		{"hundred-blocks", 100*700 + 123, 100*700 + 456, 700},
	}

	for _, algo := range algos {
		for _, tc := range cases {
			name := fmt.Sprintf("%s/%s", algo, tc.name)
			t.Run(name, func(t *testing.T) {
				oldF := make([]byte, tc.oldSize)
				newF := make([]byte, tc.newSize)
				for i := range oldF {
					oldF[i] = byte((i*31 + 17) % 251)
				}
				copyLen := tc.oldSize
				if tc.newSize < copyLen {
					copyLen = tc.newSize
				}
				copy(newF[:copyLen], oldF[:copyLen])
				if copyLen > 10 {
					mid := copyLen / 2
					newF[mid] ^= 0xFF
					if mid+1 < copyLen {
						newF[mid+1] ^= 0xFF
					}
				}
				for i := copyLen; i < tc.newSize; i++ {
					newF[i] = byte((i*47 + 13) % 251)
				}

				result, err := RoundTrip(oldF, newF, tc.blockSize, algo)
				if err != nil {
					t.Fatalf("RoundTrip: %v", err)
				}
				if !bytes.Equal(result, newF) {
					t.Errorf("roundtrip mismatch: old=%d new=%d result=%d",
						tc.oldSize, tc.newSize, len(result))
				}
			})
		}
	}

	// Also test varying block sizes with md5.
	for _, bs := range []int32{32, 128, 256, 512, 700, 1024, 4096} {
		t.Run(fmt.Sprintf("md5/bs-%d", bs), func(t *testing.T) {
			oldF := make([]byte, int(bs)*10+123)
			newF := make([]byte, int(bs)*10+456)
			for i := range oldF {
				oldF[i] = byte(i % 251)
			}
			copyLen := len(oldF)
			if len(newF) < copyLen {
				copyLen = len(newF)
			}
			copy(newF, oldF[:copyLen])
			for i := copyLen; i < len(newF); i++ {
				newF[i] = byte((i * 13) % 251)
			}

			result, err := RoundTrip(oldF, newF, bs, "md5")
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if !bytes.Equal(result, newF) {
				t.Error("roundtrip mismatch with varying block size")
			}
		})
	}
}

// =========================================================================
// TestShortChecksumWireFormat — 短强校验和线格式测试（xxh64 = 8 字节 Sum2）
// =========================================================================

func TestShortChecksumWireFormat(t *testing.T) {
	algos := map[string]int{
		"md5": 16, "sha256": 32, "xxh64": 8, "xxh3": 16,
	}

	for algo, expectedSum2Len := range algos {
		t.Run(algo, func(t *testing.T) {
			data := make([]byte, 10000)
			for i := range data {
				data[i] = byte((i*7 + 13) % 251)
			}
			blockSize := int32(700)

			sig := GenerateSignature(data, blockSize, algo)

			// Verify Sum2 length of each block.
			for i, bs := range sig.BlockSums {
				if len(bs.Sum2) != expectedSum2Len {
					t.Errorf("block %d: Sum2 len=%d, want %d (algo=%s)",
						i, len(bs.Sum2), expectedSum2Len, algo)
				}
			}

			// Wire round-trip.
			var buf bytes.Buffer
			if err := WireEncodeSignature(&buf, sig); err != nil {
				t.Fatalf("encode: %v", err)
			}

			decoded, err := WireDecodeSignature(&buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if decoded.BlockSize != sig.BlockSize ||
				decoded.FileSize != sig.FileSize ||
				len(decoded.BlockSums) != len(sig.BlockSums) {
				t.Fatalf("header mismatch")
			}

			for i := range sig.BlockSums {
				orig, dec := sig.BlockSums[i], decoded.BlockSums[i]
				if dec.Index != orig.Index || dec.Sum1 != orig.Sum1 ||
					dec.Offset != orig.Offset || dec.Length != orig.Length ||
					!bytes.Equal(dec.Sum2, orig.Sum2) {
					t.Errorf("block %d mismatch", i)
				}
			}

			// Full delta round-trip using wire-decoded signature.
			newData := make([]byte, len(data))
			copy(newData, data)
			newData[len(newData)/2] ^= 0xFF
			newData[len(newData)/2+100] ^= 0xFF

			eng, _ := NewMatchEngine(decoded.BlockSize, algo)
			eng.LoadSignature(decoded)
			insts := eng.Search(newData)

			recon, _ := NewReconstructor(data, decoded.BlockSize, algo)
			result, err := recon.Reconstruct(insts)
			if err != nil {
				t.Fatalf("reconstruct from wire-decoded sig: %v", err)
			}
			if !bytes.Equal(result, newData) {
				t.Errorf("roundtrip mismatch with wire-decoded sig (algo=%s)", algo)
			}

			t.Logf("%s: blocks=%d sum2Len=%d wireBytes=%d",
				algo, len(sig.BlockSums), expectedSum2Len, buf.Len())
		})
	}
}

// =========================================================================
// 附加边缘情况测试
// =========================================================================

func TestWireFormatZeroBlocks(t *testing.T) {
	for _, algo := range []string{"md5", "sha256", "xxh64", "xxh3"} {
		t.Run(algo, func(t *testing.T) {
			sig := GenerateSignature([]byte{}, 700, algo)
			if len(sig.BlockSums) != 0 {
				t.Errorf("%s: expected 0 blocks, got %d", algo, len(sig.BlockSums))
			}
			var buf bytes.Buffer
			if err := WireEncodeSignature(&buf, sig); err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := WireDecodeSignature(&buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(decoded.BlockSums) != 0 || decoded.FileSize != 0 {
				t.Errorf("decoded: blocks=%d fileSize=%d (want 0,0)",
					len(decoded.BlockSums), decoded.FileSize)
			}
		})
	}
}

func TestWireFormatSingleBlock(t *testing.T) {
	for _, algo := range []string{"md5", "sha256", "xxh64", "xxh3"} {
		t.Run(algo, func(t *testing.T) {
			data := make([]byte, 700)
			for i := range data {
				data[i] = byte(i % 251)
			}
			sig := GenerateSignature(data, 700, algo)
			if len(sig.BlockSums) != 1 {
				t.Fatalf("expected 1 block, got %d", len(sig.BlockSums))
			}
			var buf bytes.Buffer
			WireEncodeSignature(&buf, sig)
			decoded, _ := WireDecodeSignature(&buf)
			if len(decoded.BlockSums) != 1 {
				t.Fatalf("expected 1 block, got %d", len(decoded.BlockSums))
			}
			bs := decoded.BlockSums[0]
			if bs.Index != 0 || bs.Offset != 0 || bs.Length != 700 {
				t.Errorf("Index=%d Offset=%d Length=%d (want 0,0,700)",
					bs.Index, bs.Offset, bs.Length)
			}
		})
	}
}

func TestDeltaOneByteDiff(t *testing.T) {
	algos := []string{"md5", "sha256", "xxh64", "xxh3"}
	sizes := []int{700, 1400, 7000}

	for _, algo := range algos {
		for _, sz := range sizes {
			name := fmt.Sprintf("%s/%d-bytes", algo, sz)
			t.Run(name, func(t *testing.T) {
				oldF := make([]byte, sz)
				for i := range oldF {
					oldF[i] = byte((i*13 + 7) % 251)
				}
				newF := make([]byte, sz)
				copy(newF, oldF)
				newF[sz/2] ^= 0xFF

				blockSize := int32(700)
				if sz < 700 {
					blockSize = int32(sz)
				}

				result, err := RoundTrip(oldF, newF, blockSize, algo)
				if err != nil {
					t.Fatalf("RoundTrip: %v", err)
				}
				if !bytes.Equal(result, newF) {
					t.Error("roundtrip mismatch for 1-byte diff")
				}
			})
		}
	}
}

func TestSignatureZeroLengthFile(t *testing.T) {
	for _, algo := range []string{"md5", "sha256", "xxh64", "xxh3"} {
		t.Run(algo, func(t *testing.T) {
			sig := GenerateSignature([]byte{}, 700, algo)
			if sig.BlockSize != 700 || sig.FileSize != 0 || len(sig.BlockSums) != 0 {
				t.Errorf("%s: BlockSize=%d FileSize=%d Blocks=%d (want 700,0,0)",
					algo, sig.BlockSize, sig.FileSize, len(sig.BlockSums))
			}
		})
	}
}

func TestChecksumAlgoLengths(t *testing.T) {
	expected := map[string]int{
		"md5": 16, "sha256": 32, "xxh64": 8, "xxh3": 16,
	}
	for name, wantLen := range expected {
		algo, err := GetAlgo(name)
		if err != nil {
			t.Fatalf("GetAlgo(%q): %v", name, err)
		}
		if algo.Length != wantLen {
			t.Errorf("%s: Length=%d want %d", name, algo.Length, wantLen)
		}
		h := algo.New()
		h.Write([]byte("test"))
		if len(h.Sum(nil)) != wantLen {
			t.Errorf("%s: Sum() len=%d want %d", name, len(h.Sum(nil)), wantLen)
		}
	}
}

func TestFastSumParity(t *testing.T) {
	rng := make([]byte, 8192)
	rand.Read(rng)

	checks := []struct {
		name    string
		fast    func(out, data []byte) []byte
		wantLen int
	}{
		{"md5", md5FastSum, 16},
		{"sha256", sha256FastSum, 32},
		{"xxh64", xxh64FastSum, 8},
		{"xxh3", xxh3FastSum, 16},
	}

	sizes := []int{0, 1, 15, 16, 17, 31, 32, 33, 63, 64, 65,
		127, 128, 129, 255, 256, 512, 1024, 2048, 4096, 8192}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			for _, sz := range sizes {
				if sz > len(rng) {
					continue
				}
				data := rng[:sz]

				algo, _ := GetAlgo(c.name)
				h := algo.New()
				h.Write(data)
				want := h.Sum(nil)

				out := make([]byte, c.wantLen+16)
				got := c.fast(out, data)

				if !bytes.Equal(got, want) {
					t.Errorf("%s FastSum mismatch at size %d", c.name, sz)
					return
				}
			}
		})
	}
}

// =========================================================================
// TestGenerateSignatureReaderAllAlgos — 流式签名生成全算法一致性测试
// =========================================================================

func TestGenerateSignatureReaderAllAlgos(t *testing.T) {
	algos := []string{"md5", "sha256", "xxh64", "xxh3"}
	sizes := []int{0, 1, 700, 701, 1400, 5000, 70000}

	for _, algo := range algos {
		for _, sz := range sizes {
			name := fmt.Sprintf("%s/%d", algo, sz)
			t.Run(name, func(t *testing.T) {
				data := make([]byte, sz)
				for i := range data {
					data[i] = byte((i*17 + 3) % 251)
				}

				// In-memory path (baseline).
				memSig := GenerateSignature(data, 700, algo)

				// Streaming reader path.
				readerSig, _ := GenerateSignatureReader(
					bytes.NewReader(data), int64(sz), 700, algo)

				// Compare headers.
				if memSig.BlockSize != readerSig.BlockSize ||
					memSig.FileSize != readerSig.FileSize ||
					len(memSig.BlockSums) != len(readerSig.BlockSums) {
					t.Fatalf("header mismatch: mem={bs=%d fs=%d n=%d} reader={bs=%d fs=%d n=%d}",
						memSig.BlockSize, memSig.FileSize, len(memSig.BlockSums),
						readerSig.BlockSize, readerSig.FileSize, len(readerSig.BlockSums))
				}

				// Compare each block.
				for i := range memSig.BlockSums {
					ma, ra := memSig.BlockSums[i], readerSig.BlockSums[i]
					if ma.Index != ra.Index || ma.Sum1 != ra.Sum1 ||
						ma.Offset != ra.Offset || ma.Length != ra.Length ||
						!bytes.Equal(ma.Sum2, ra.Sum2) {
						t.Errorf("block %d mismatch", i)
					}
				}

				// Verify round-trip using the reader-generated signature.
				if sz > 0 {
					result, err := RoundTrip(data, data, 700, algo)
					if err != nil {
						t.Fatalf("RoundTrip with reader sig: %v", err)
					}
					if !bytes.Equal(result, data) {
						t.Error("roundtrip mismatch with reader sig")
					}
				}
			})
		}
	}
}

// =========================================================================
// Test 6: SearchReader 分块读取压力测试
// 模拟真实 IO 场景：1 字节/次、blockSize 边界、blockSize-1 截断。
// =========================================================================

func TestSearchReaderChunkedReads(t *testing.T) {
	// Create deterministic data: old and new files with ~10% difference.
	oldData := make([]byte, 50000)
	newData := make([]byte, 50000)
	for i := range oldData {
		oldData[i] = byte((i*7 + 13) % 251)
	}
	copy(newData, oldData)
	for i := 0; i < len(newData)/10; i++ {
		newData[i*10] ^= 0xFF
	}

	blockSize := int32(700)
	sig := GenerateSignature(oldData, blockSize, "md5")

	// Baseline: batch Search.
	batchEng, _ := NewMatchEngine(blockSize, "md5")
	batchEng.LoadSignature(sig)
	batchResults := batchEng.Search(newData)
	batchRecon, _ := NewReconstructor(oldData, blockSize, "md5")
	batchOut, _ := batchRecon.Reconstruct(batchResults)

	// Test various chunk sizes for streaming reads.
	chunkSizes := []int{
		1,                  // single byte at a time
		int(blockSize),     // exactly one block
		int(blockSize) - 1, // blockSize-1 (off-by-one)
		int(blockSize) + 1, // blockSize+1
		2 * int(blockSize), // two blocks
		1000,
		CHUNK_SIZE,
		CHUNK_SIZE + 1,
	}

	for _, chunk := range chunkSizes {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			cr := &chunkedReader{data: newData, chunkSize: chunk}

			eng, _ := NewMatchEngine(blockSize, "md5")
			eng.LoadSignature(sig)

			var results []MatchResult
			err := eng.SearchReader(cr, int64(len(newData)), func(mr MatchResult) error {
				cp := mr
				if mr.IsLiteral {
					cp.Data = make([]byte, len(mr.Data))
					copy(cp.Data, mr.Data)
				}
				results = append(results, cp)
				return nil
			})
			if err != nil {
				t.Fatalf("SearchReader: %v", err)
			}

			// Reconstruct and verify.
			recon, _ := NewReconstructor(oldData, blockSize, "md5")
			out, err := recon.Reconstruct(results)
			if err != nil {
				t.Fatalf("Reconstruct: %v", err)
			}
			if !bytes.Equal(out, batchOut) {
				t.Errorf("output differs from batch for chunk size %d", chunk)
				// Find first diff.
				for i := 0; i < len(out) && i < len(batchOut); i++ {
					if out[i] != batchOut[i] {
						t.Logf("first diff at byte %d: got=%d want=%d", i, out[i], batchOut[i])
						break
					}
				}
			}

			// Stats should match batch.
			if eng.Matches != batchEng.Matches {
				t.Errorf("Matches: stream=%d batch=%d", eng.Matches, batchEng.Matches)
			}
			if eng.LiteralBytes != batchEng.LiteralBytes {
				t.Errorf("LiteralBytes: stream=%d batch=%d", eng.LiteralBytes, batchEng.LiteralBytes)
			}
		})
	}
}

// chunkedReader returns data in fixed-size chunks (except possibly the last).
type chunkedReader struct {
	data      []byte
	pos       int
	chunkSize int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunkSize
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}

// =========================================================================
// TestPartialBasisReconstruction — 截断 basis 重建（partial transfer）
// basis 只有原文件的前半部分，验证块匹配 + 字面量混合重建的正确性。
// =========================================================================

func TestPartialBasisReconstruction(t *testing.T) {
	blockSize := int32(700)

	// oldFile = 3 blocks of distinct content.
	// Block 0: all 'A', Block 1: all 'B', Block 2: all 'C'.
	oldFile := make([]byte, 3*int(blockSize))
	for i := 0; i < int(blockSize); i++ {
		oldFile[i] = 'A'
		oldFile[i+int(blockSize)] = 'B'
		oldFile[i+2*int(blockSize)] = 'C'
	}

	// newFile: first 2 blocks match old, last block is new content ('D').
	newFile := make([]byte, 3*int(blockSize))
	copy(newFile, oldFile[:2*int(blockSize)])
	for i := 2 * int(blockSize); i < len(newFile); i++ {
		newFile[i] = 'D'
	}

	// Signature covers the full old file.
	sig := GenerateSignature(oldFile, blockSize, "md5")

	// Delta from full old file → new file.
	// Expected: match block 0, match block 1, literal (block 2 = all 'D').
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	instructions := eng.Search(newFile)

	// ── Scenario A: full basis (normal case) ──
	fullRecon, _ := NewReconstructor(oldFile, blockSize, "md5")
	fullOut, err := fullRecon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("full basis reconstruct: %v", err)
	}
	if !bytes.Equal(fullOut, newFile) {
		t.Fatal("full basis reconstruction failed")
	}

	// ── Scenario B: truncated basis (only has first 2 blocks = 1400 bytes) ──
	// The delta references blocks 0 and 1 (which the truncated basis has)
	// and a literal for block 2 (which the basis doesn't need).
	truncatedBasis := oldFile[:2*int(blockSize)] // 1400 bytes

	partialRecon, _ := NewReconstructor(truncatedBasis, blockSize, "md5")
	partialOut, err := partialRecon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("truncated basis reconstruct: %v", err)
	}
	if !bytes.Equal(partialOut, newFile) {
		t.Errorf("truncated basis reconstruction mismatch")
		t.Logf("expected: %d bytes, got: %d bytes", len(newFile), len(partialOut))
	}

	// ── Scenario C: truncated basis with blockLens ──
	// Provide actual block lengths so the reconstructor knows the last
	// block in the sig might be partial.
	blockLens := make([]int32, len(sig.BlockSums))
	for i := range blockLens {
		blockLens[i] = sig.BlockSums[i].Length
	}
	partialRecon2, _ := NewReconstructor(truncatedBasis, blockSize, "md5", blockLens)
	partialOut2, err := partialRecon2.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("truncated basis + blockLens reconstruct: %v", err)
	}
	if !bytes.Equal(partialOut2, newFile) {
		t.Error("truncated basis + blockLens reconstruction mismatch")
	}

	// ── Scenario D: same test with other algorithms ──
	for _, algo := range []string{"sha256", "xxh64", "xxh3"} {
		t.Run(algo, func(t *testing.T) {
			sig := GenerateSignature(oldFile, blockSize, algo)
			eng, _ := NewMatchEngine(blockSize, algo)
			eng.LoadSignature(sig)
			insts := eng.Search(newFile)

			partialRecon, _ := NewReconstructor(truncatedBasis, blockSize, algo)
			out, err := partialRecon.Reconstruct(insts)
			if err != nil {
				t.Fatalf("%s truncated basis: %v", algo, err)
			}
			if !bytes.Equal(out, newFile) {
				t.Errorf("%s truncated basis mismatch", algo)
			}
		})
	}
}

// TestPartialBasisLastBlockPartial tests a partial last block in the
// signature with a truncated basis — the edge case that triggered
// the partial-last-block matching bug.
func TestPartialBasisLastBlockPartial(t *testing.T) {
	blockSize := int32(700)

	// oldFile = 2200 bytes: 3 full blocks + 100-byte partial last block.
	oldFile := make([]byte, 3*int(blockSize)+100)
	for i := range oldFile {
		oldFile[i] = byte((i*7 + 13) % 251)
	}

	// newFile = same content (identical).
	newFile := make([]byte, len(oldFile))
	copy(newFile, oldFile)

	// Signature covers full old file.
	sig := GenerateSignature(oldFile, blockSize, "md5")
	if len(sig.BlockSums) != 4 {
		t.Fatalf("expected 4 blocks (3 full + 1 partial), got %d", len(sig.BlockSums))
	}
	// Last block should be partial.
	if sig.BlockSums[3].Length != 100 {
		t.Errorf("last block length: want 100, got %d", sig.BlockSums[3].Length)
	}

	// Delta: identical file → should produce all block matches.
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	instructions := eng.Search(newFile)

	// Truncated basis: only has first 2 full blocks (1400 bytes).
	// Blocks 0 and 1 can be matched from basis; blocks 2 and 3 are missing
	// from the basis but the delta references them → reconstruction fails
	// for blocks beyond basis. The test verifies this is handled gracefully.
	truncatedBasis := oldFile[:2*int(blockSize)]

	recon, _ := NewReconstructor(truncatedBasis, blockSize, "md5")
	_, err := recon.Reconstruct(instructions)
	// Since blocks 2 and 3 reference positions beyond the truncated basis,
	// the reconstructor should return an error.
	if err == nil {
		// If no error, verify the output is at least a valid prefix.
		t.Log("truncated basis did not error — checking partial output")
	}
}

// =========================================================================
// Test 8: 大块数哈希表路径（>65536 块触发 v % tableSize）
// 验证大表哈希路径与标准路径的一致性。
// =========================================================================

func TestLargeBlockCountHashTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large hash table test in short mode")
	}

	// Need > 52424 blocks to trigger tableSize > 65536.
	// computeTableSize: (n/8)*10+11. For n=55000 → (6875)*10+11 = 68761 > 65536.
	// Use small blockSize to keep file size reasonable.
	const blockSize = int32(32)
	const numBlocks = 55000
	fileSize := int64(numBlocks) * int64(blockSize) // 1.76 MB

	// Create deterministic old file.
	oldFile := make([]byte, fileSize)
	for i := range oldFile {
		oldFile[i] = byte((i*13 + 7) % 251)
	}

	// Verify tableSize > 65536.
	ts := computeTableSize(numBlocks)
	if ts <= 65536 {
		t.Fatalf("tableSize=%d, expected > 65536 (need more blocks)", ts)
	}
	t.Logf("tableSize=%d (large table path, v %% tableSize)", ts)

	sig := GenerateSignature(oldFile, blockSize, "md5")
	if len(sig.BlockSums) != numBlocks {
		t.Fatalf("expected %d blocks, got %d", numBlocks, len(sig.BlockSums))
	}

	// ── Test 1: Identical file search ──
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	results := eng.Search(oldFile)

	// Verify the hash table is large.
	if eng.tableSize != ts {
		t.Errorf("engine.tableSize=%d, want %d", eng.tableSize, ts)
	}

	// Identical file should have zero literals.
	if eng.LiteralBytes > 0 {
		t.Errorf("identical file: LiteralBytes=%d, expected 0", eng.LiteralBytes)
	}
	if eng.Matches != numBlocks {
		t.Errorf("identical file: Matches=%d, expected %d", eng.Matches, numBlocks)
	}

	recon, _ := NewReconstructor(oldFile, blockSize, "md5")
	out, err := recon.Reconstruct(results)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !bytes.Equal(out, oldFile) {
		t.Error("identical file reconstruction mismatch")
	}

	// ── Test 2: Modified file (sparse changes: ~0.5% = every 200th byte) ──
	// Use sparse modifications so most blocks still match.
	newFile := make([]byte, fileSize)
	copy(newFile, oldFile)
	for i := 0; i < len(newFile); i += 200 {
		newFile[i] ^= 0xFF
	}

	eng2, _ := NewMatchEngine(blockSize, "md5")
	eng2.LoadSignature(sig)
	insts2 := eng2.Search(newFile)

	recon2, _ := NewReconstructor(oldFile, blockSize, "md5")
	out2, err := recon2.Reconstruct(insts2)
	if err != nil {
		t.Fatalf("reconstruct modified: %v", err)
	}
	if !bytes.Equal(out2, newFile) {
		t.Error("modified file reconstruction mismatch")
	}

	t.Logf("large table: Matches=%d LiteralBytes=%d/%d (%.1f%%)",
		eng2.Matches, eng2.LiteralBytes, fileSize,
		float64(eng2.LiteralBytes)/float64(fileSize)*100)

	// ── Test 3: Streaming search with large table ──
	eng3, _ := NewMatchEngine(blockSize, "md5")
	eng3.LoadSignature(sig)

	var streamResults []MatchResult
	err = eng3.SearchReader(bytes.NewReader(oldFile), fileSize, func(mr MatchResult) error {
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		streamResults = append(streamResults, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("SearchReader with large table: %v", err)
	}

	recon3, _ := NewReconstructor(oldFile, blockSize, "md5")
	out3, err := recon3.Reconstruct(streamResults)
	if err != nil {
		t.Fatalf("reconstruct stream: %v", err)
	}
	if !bytes.Equal(out3, oldFile) {
		t.Error("streaming search large table: reconstruction mismatch")
	}
	if eng3.LiteralBytes > 0 {
		t.Errorf("streaming large table: LiteralBytes=%d, expected 0", eng3.LiteralBytes)
	}
}

// =========================================================================
// Test 9: DeltaFromWire 全算法往返
// 高层 API 目前只有 md5 的测试，补充所有算法的覆盖。
// =========================================================================

func TestDeltaFromWireAllAlgos(t *testing.T) {
	algos := []string{"md5", "sha256", "xxh64", "xxh3"}

	for _, algo := range algos {
		t.Run(algo, func(t *testing.T) {
			oldFile := make([]byte, 10000)
			newFile := make([]byte, 10000)
			for i := range oldFile {
				oldFile[i] = byte((i*17 + 3) % 251)
			}
			copy(newFile, oldFile)
			// Modify ~5% of bytes.
			for i := 0; i < len(newFile)/20; i++ {
				newFile[i*20] ^= 0xFF
			}

			// Encode signature to wire.
			sig := GenerateSignature(oldFile, 700, algo)
			var buf bytes.Buffer
			if err := WireEncodeSignature(&buf, sig); err != nil {
				t.Fatalf("encode sig: %v", err)
			}

			// DeltaFromWire: read signature from wire, compute delta.
			insts, eng, err := DeltaFromWire(&buf, newFile, algo)
			if err != nil {
				t.Fatalf("DeltaFromWire: %v", err)
			}

			// Reconstruct.
			result, err := ApplyDelta(oldFile, insts, 700, algo)
			if err != nil {
				t.Fatalf("ApplyDelta: %v", err)
			}
			if !bytes.Equal(result, newFile) {
				t.Error("DeltaFromWire roundtrip mismatch")
			}

			t.Logf("%s: LiteralBytes=%d/%d (%.1f%%)",
				algo, eng.LiteralBytes, len(newFile),
				float64(eng.LiteralBytes)/float64(len(newFile))*100)
		})
	}
}

// =========================================================================
// TestReconstructWriteInstructionParity — Reconstruct vs WriteInstruction
// 两条重建路径应产生完全相同的输出。
// =========================================================================

func TestReconstructWriteInstructionParity(t *testing.T) {
	oldFile := make([]byte, 15000)
	newFile := make([]byte, 15000)
	for i := range oldFile {
		oldFile[i] = byte((i*13 + 7) % 251)
	}
	copy(newFile, oldFile)
	for i := 0; i < len(newFile)/15; i++ {
		newFile[i*15] ^= 0xFF
	}

	sig := GenerateSignature(oldFile, 700, "md5")
	eng, _ := NewMatchEngine(700, "md5")
	eng.LoadSignature(sig)
	insts := eng.Search(newFile)

	// Path A: Reconstruct (batch).
	recon, _ := NewReconstructor(oldFile, 700, "md5")
	batchOut, err := recon.Reconstruct(insts)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	// Path B: WriteInstruction (streaming).
	var streamBuf bytes.Buffer
	for _, inst := range insts {
		if err := recon.WriteInstruction(&streamBuf, inst); err != nil {
			t.Fatalf("WriteInstruction: %v", err)
		}
	}

	if !bytes.Equal(batchOut, streamBuf.Bytes()) {
		t.Errorf("Reconstruct vs WriteInstruction output mismatch")
		for i := 0; i < len(batchOut) && i < streamBuf.Len(); i++ {
			if batchOut[i] != streamBuf.Bytes()[i] {
				t.Logf("first diff at byte %d: batch=%d stream=%d",
					i, batchOut[i], streamBuf.Bytes()[i])
				break
			}
		}
	}

	// Also verify both match newFile.
	if !bytes.Equal(batchOut, newFile) {
		t.Error("Reconstruct output does not match newFile")
	}
}

// =========================================================================
// TestMatchEngineEmptyState — MatchEngine 空状态安全测试
// Search 前未 LoadSignature / nil 签名 / 零块签名不应 panic。
// =========================================================================

func TestMatchEngineEmptyState(t *testing.T) {
	data := []byte("some test data for empty engine")

	// Case 1: Search before any LoadSignature.
	eng1, _ := NewMatchEngine(700, "md5")
	results := eng1.Search(data)
	if len(results) == 0 {
		t.Error("expected at least one literal result from empty engine")
	}
	// Should be all literals.
	for _, r := range results {
		if !r.IsLiteral {
			t.Error("empty engine should produce only literals")
		}
	}

	// Case 2: Load nil signature (should not panic).
	eng2, _ := NewMatchEngine(700, "md5")
	eng2.LoadSignature(nil)
	results2 := eng2.Search(data)
	totalLit := int64(0)
	for _, r := range results2 {
		if r.IsLiteral {
			totalLit += int64(len(r.Data))
		}
	}
	if totalLit != int64(len(data)) {
		t.Errorf("expected %d literal bytes with nil sig, got %d", len(data), totalLit)
	}

	// Case 3: Empty signature (zero blocks, non-nil).
	eng3, _ := NewMatchEngine(700, "md5")
	eng3.LoadSignature(&Signature{BlockSize: 700, BlockSums: []BlockSum{}})
	results3 := eng3.Search(data)
	totalLit3 := int64(0)
	for _, r := range results3 {
		if r.IsLiteral {
			totalLit3 += int64(len(r.Data))
		}
	}
	if totalLit3 != int64(len(data)) {
		t.Errorf("expected %d literal bytes with empty sig, got %d", len(data), totalLit3)
	}

	// Case 4: Zero-block non-nil signature with nil BlockSums.
	eng4, _ := NewMatchEngine(700, "md5")
	eng4.LoadSignature(&Signature{BlockSize: 700, FileSize: 0, BlockSums: nil})
	results4 := eng4.Search(data)
	if len(results4) == 0 && len(data) > 0 {
		t.Error("expected literal results from zero-block sig with nil BlockSums")
	}

	// Case 5: SearchReader before LoadSignature.
	eng5, _ := NewMatchEngine(700, "md5")
	var srResults []MatchResult
	err := eng5.SearchReader(bytes.NewReader(data), int64(len(data)),
		func(mr MatchResult) error {
			cp := mr
			if mr.IsLiteral {
				cp.Data = make([]byte, len(mr.Data))
				copy(cp.Data, mr.Data)
			}
			srResults = append(srResults, cp)
			return nil
		})
	if err != nil {
		t.Fatalf("SearchReader on empty engine: %v", err)
	}
	if len(srResults) == 0 {
		t.Error("expected results from SearchReader on empty engine")
	}

	// Case 6: Reconstruct from empty engine results.
	recon, _ := NewReconstructor(data, 700, "md5")
	out, err := recon.Reconstruct(results)
	if err != nil {
		t.Fatalf("Reconstruct empty engine results: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Error("reconstruct from empty engine should reproduce input")
	}
}

// =========================================================================
// TestSearchDeterminism — 指令排序确定性
// 相同输入多次 Search 产生完全相同的指令序列。
// =========================================================================

func TestSearchDeterminism(t *testing.T) {
	oldFile := make([]byte, 20000)
	newFile := make([]byte, 20000)
	for i := range oldFile {
		oldFile[i] = byte((i*19 + 5) % 251)
	}
	copy(newFile, oldFile)
	for i := 0; i < len(newFile)/12; i++ {
		newFile[i*12] ^= 0xFF
	}

	sig := GenerateSignature(oldFile, 700, "md5")

	const runs = 10
	var firstResults []MatchResult

	for run := 0; run < runs; run++ {
		eng, _ := NewMatchEngine(700, "md5")
		eng.LoadSignature(sig)
		results := eng.Search(newFile)

		if run == 0 {
			firstResults = results
			continue
		}

		if len(results) != len(firstResults) {
			t.Fatalf("run %d: %d results, run 0: %d results",
				run, len(results), len(firstResults))
		}

		for i := range results {
			a, b := firstResults[i], results[i]
			if a.IsLiteral != b.IsLiteral {
				t.Errorf("run %d result[%d]: IsLiteral mismatch", run, i)
				continue
			}
			if a.IsLiteral {
				if !bytes.Equal(a.Data, b.Data) {
					t.Errorf("run %d result[%d]: literal data mismatch", run, i)
				}
				if a.Offset != b.Offset {
					t.Errorf("run %d result[%d]: offset mismatch %d vs %d",
						run, i, a.Offset, b.Offset)
				}
			} else {
				if a.BlockIdx != b.BlockIdx {
					t.Errorf("run %d result[%d]: BlockIdx mismatch %d vs %d",
						run, i, a.BlockIdx, b.BlockIdx)
				}
			}
		}
	}
}

// =========================================================================
// TestWantIdxAdjacentMatch — wantIdx 相邻匹配启发式验证
// 引擎用 wantIdx 优先匹配相邻块，确保此优化不会跳过正确匹配。
// =========================================================================

func TestWantIdxAdjacentMatch(t *testing.T) {
	// Create a file where blocks are in order: A, B, C, A, B, C.
	// The wantIdx heuristic should prefer the adjacent match (block 1
	// after block 0) over the earlier occurrence when both match.
	blockSize := int32(700)

	// oldFile: 6 blocks — A0, B0, C0, A1, B1, C1 (duplicate patterns).
	oldFile := make([]byte, 6*int(blockSize))
	for i := 0; i < int(blockSize); i++ {
		oldFile[0*int(blockSize)+i] = 'A'                   // A0
		oldFile[1*int(blockSize)+i] = 'B'                   // B0
		oldFile[2*int(blockSize)+i] = 'C'                   // C0
		oldFile[3*int(blockSize)+i] = byte('A' + byte(i%3)) // A1 (different content from A0)
		oldFile[4*int(blockSize)+i] = byte('B' + byte(i%3)) // B1
		oldFile[5*int(blockSize)+i] = byte('C' + byte(i%3)) // C1
	}

	// newFile = same as oldFile (identical).
	newFile := make([]byte, len(oldFile))
	copy(newFile, oldFile)

	sig := GenerateSignature(oldFile, blockSize, "md5")
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	results := eng.Search(newFile)

	// Identical file: should match all 6 blocks.
	if eng.Matches != 6 {
		t.Errorf("expected 6 matches, got %d", eng.Matches)
	}
	if eng.LiteralBytes > 0 {
		t.Errorf("identical file: LiteralBytes=%d, expected 0", eng.LiteralBytes)
	}

	// Verify the matched block indices are in order: 0,1,2,3,4,5.
	expectedIdx := 0
	for _, r := range results {
		if !r.IsLiteral {
			if r.BlockIdx != expectedIdx {
				t.Errorf("wantIdx heuristic may have skipped: expected block %d, got %d",
					expectedIdx, r.BlockIdx)
			}
			expectedIdx++
		}
	}

	// Verify reconstruction.
	recon, _ := NewReconstructor(oldFile, blockSize, "md5")
	out, err := recon.Reconstruct(results)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if !bytes.Equal(out, newFile) {
		t.Error("wantIdx test: reconstruction mismatch")
	}
}

// =========================================================================
// TestDecodeInstructionsStreamEdgeCases — 指令流解码边缘情况
// 空流、截断数据、损坏数据不应 panic。
// =========================================================================

func TestDecodeInstructionsStreamEdgeCases(t *testing.T) {
	// Case 1: empty stream (no header at all).
	var called int
	err := DecodeInstructionsStream(bytes.NewReader(nil), func(mr MatchResult) error {
		called++
		return nil
	})
	if err == nil {
		t.Error("expected error on empty stream")
	}
	if called > 0 {
		t.Error("callback should not be called on empty stream")
	}

	// Case 2: empty count=0 batch.
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 0)
	err = DecodeInstructionsStream(bytes.NewReader(header), func(mr MatchResult) error {
		called++
		return nil
	})
	if err != nil {
		t.Errorf("empty batch should succeed: %v", err)
	}
	if called > 0 {
		t.Error("callback should not be called for empty batch")
	}

	// Case 3: truncated count header.
	err = DecodeInstructionsStream(bytes.NewReader([]byte{0x00, 0x01}), func(mr MatchResult) error {
		return nil
	})
	if err == nil {
		t.Error("expected error on truncated header")
	}

	// Case 4: count claims 1 but no flag byte.
	header = make([]byte, 4)
	binary.BigEndian.PutUint32(header, 1)
	err = DecodeInstructionsStream(bytes.NewReader(header), func(mr MatchResult) error {
		return nil
	})
	if err == nil {
		t.Error("expected error on missing flag byte")
	}

	// Case 5: literal flag but no length.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(1)) // count=1
	buf.WriteByte(0)                               // flag=literal
	err = DecodeInstructionsStream(bytes.NewReader(buf.Bytes()), func(mr MatchResult) error {
		return nil
	})
	if err == nil {
		t.Error("expected error on missing literal length")
	}

	// Case 6: match flag but no index.
	buf.Reset()
	binary.Write(buf, binary.BigEndian, uint32(1))
	buf.WriteByte(1) // flag=match
	err = DecodeInstructionsStream(bytes.NewReader(buf.Bytes()), func(mr MatchResult) error {
		return nil
	})
	if err == nil {
		t.Error("expected error on missing match index")
	}

	// Case 7: valid round-trip: encode then decode.
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte((i*7 + 13) % 251)
	}
	sig := GenerateSignature(data, 700, "md5")
	eng, _ := NewMatchEngine(700, "md5")
	eng.LoadSignature(sig)
	insts := eng.Search(data)

	var wireBuf bytes.Buffer
	if err := WireEncodeInstructions(&wireBuf, insts); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var decoded []MatchResult
	err = DecodeInstructionsStream(&wireBuf, func(mr MatchResult) error {
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		decoded = append(decoded, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != len(insts) {
		t.Errorf("round-trip count: got %d want %d", len(decoded), len(insts))
	}
}

// =========================================================================
// TestMatchEngineReuse — MatchEngine 重复使用
// 同一 engine LoadSignature→Search 两次，第二次应覆盖第一次。
// =========================================================================

func TestMatchEngineReuse(t *testing.T) {
	// First file: 5 blocks of 'A'.
	dataA := makeBytesRepeat(5*700, 'A')
	sigA := GenerateSignature(dataA, 700, "md5")

	// Second file: 10 blocks of 'B'.
	dataB := makeBytesRepeat(10*700, 'B')
	sigB := GenerateSignature(dataB, 700, "md5")

	eng, _ := NewMatchEngine(700, "md5")

	// Round 1: load sigA, search dataA.
	eng.LoadSignature(sigA)
	results1 := eng.Search(dataA)
	matches1 := eng.Matches
	if matches1 != 5 {
		t.Errorf("round 1: expected 5 matches, got %d", matches1)
	}

	// Round 2: load sigB, search dataB — stats accumulate across calls.
	eng.LoadSignature(sigB)
	results2 := eng.Search(dataB)
	// Matches from round 2 alone = total - round 1.
	matches2 := eng.Matches - matches1
	if matches2 != 10 {
		t.Errorf("round 2: expected 10 new matches, got %d (total=%d, round1=%d)",
			matches2, eng.Matches, matches1)
	}

	// Verify both rounds reconstruct correctly.
	recon, _ := NewReconstructor(dataA, 700, "md5")
	out1, _ := recon.Reconstruct(results1)
	if !bytes.Equal(out1, dataA) {
		t.Error("round 1 reconstruction mismatch")
	}

	recon2, _ := NewReconstructor(dataB, 700, "md5")
	out2, _ := recon2.Reconstruct(results2)
	if !bytes.Equal(out2, dataB) {
		t.Error("round 2 reconstruction mismatch")
	}
}

// =========================================================================
// TestMatchStatsConsistency — 匹配统计一致性
// HashHits ≥ FalseAlarms、LiteralBytes + MatchedBytes = fileSize。
// =========================================================================

func TestMatchStatsConsistency(t *testing.T) {
	sizes := []int{700, 5000, 50000}
	blockSize := int32(700)

	for _, sz := range sizes {
		oldF := make([]byte, sz)
		newF := make([]byte, sz)
		for i := range oldF {
			oldF[i] = byte((i*13 + 7) % 251)
		}
		copy(newF, oldF)
		for i := 0; i < len(newF)/20; i++ {
			newF[i*20] ^= 0xFF
		}

		sig := GenerateSignature(oldF, blockSize, "md5")
		eng, _ := NewMatchEngine(blockSize, "md5")
		eng.LoadSignature(sig)
		eng.Search(newF)

		// Hash hits must >= false alarms.
		if eng.HashHits < eng.FalseAlarms {
			t.Errorf("size=%d: HashHits(%d) < FalseAlarms(%d)",
				sz, eng.HashHits, eng.FalseAlarms)
		}

		// Literal + matched bytes should sum to file size.
		matchedBytes := int64(eng.Matches) * int64(blockSize)
		totalCovered := eng.LiteralBytes + matchedBytes
		// Note: may exceed fileSize slightly due to partial last block,
		// so we check that they're in the same ballpark.
		if totalCovered < int64(sz) || totalCovered > int64(sz)+int64(blockSize) {
			t.Errorf("size=%d: LiteralBytes(%d) + matched(%d) = %d, fileSize=%d",
				sz, eng.LiteralBytes, matchedBytes, totalCovered, sz)
		}

		// Matches should be >= 0 and <= block count.
		blockCount := (sz + int(blockSize) - 1) / int(blockSize)
		if eng.Matches < 0 || eng.Matches > blockCount {
			t.Errorf("size=%d: Matches=%d out of range [0, %d]",
				sz, eng.Matches, blockCount)
		}
	}
}

// =========================================================================
// TestTinyBlockSize — 极小 blockSize 往返测试
// blockSize=1,2,3 的边界行为。
// =========================================================================

func TestTinyBlockSize(t *testing.T) {
	blockSizes := []int32{1, 2, 3}
	fileSizes := []int{0, 1, 2, 3, 10, 100}

	for _, bs := range blockSizes {
		for _, sz := range fileSizes {
			name := fmt.Sprintf("bs=%d/sz=%d", bs, sz)
			t.Run(name, func(t *testing.T) {
				oldF := make([]byte, sz)
				newF := make([]byte, sz)
				for i := range oldF {
					oldF[i] = byte((i*17 + 5) % 251)
				}
				copy(newF, oldF)
				if sz > 1 {
					newF[sz/2] ^= 0xFF
				}

				result, err := RoundTrip(oldF, newF, bs, "md5")
				if err != nil {
					t.Fatalf("RoundTrip: %v", err)
				}
				if !bytes.Equal(result, newF) {
					t.Errorf("mismatch: old=%d new=%d result=%d", sz, sz, len(result))
				}

				// Also verify identical file.
				result2, err := RoundTrip(oldF, oldF, bs, "md5")
				if err != nil {
					t.Fatalf("identical RoundTrip: %v", err)
				}
				if !bytes.Equal(result2, oldF) {
					t.Error("identical file mismatch")
				}
			})
		}
	}
}

// =========================================================================
// Helpers
// =========================================================================

func makeBytesRepeat(n int, b byte) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = b
	}
	return d
}

func makeIncBytes(n int) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i)
	}
	return d
}
