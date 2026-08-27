package delta

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/henryborner/go-rsync/hashsimd"
)

func TestRollingSum(t *testing.T) {
	data := []byte("Hello, World! This is a test of the rolling checksum.")
	blockSize := int32(16)

	rs1 := NewRollingSum(data[:blockSize])
	sumFull := rs1.Value()

	// Each Roll step should match a fresh Reset
	rs2 := NewRollingSum(data[1 : blockSize+1])
	if rs2.Value() != rs1.RollAndCompare(data[0], data[blockSize], blockSize) {
		t.Error("Roll result inconsistent with Reset")
	}

	_ = sumFull
}

func (rs *RollingSum) RollAndCompare(oldByte, newByte byte, blockLen int32) uint32 {
	rs.Roll(oldByte, newByte, blockLen)
	return rs.Value()
}

func TestGenerateSignature(t *testing.T) {
	data := make([]byte, 1024*10) // 10KB
	rand.Read(data)

	blockSize := int32(512)
	sig := GenerateSignature(data, blockSize, "md5")

	if sig.BlockSize != blockSize {
		t.Errorf("wrong block size: expected %d, got %d", blockSize, sig.BlockSize)
	}

	expectedBlocks := (len(data) + int(blockSize) - 1) / int(blockSize)
	if len(sig.BlockSums) != expectedBlocks {
		t.Errorf("wrong block count: expected %d, got %d", expectedBlocks, len(sig.BlockSums))
	}

	for i, bs := range sig.BlockSums {
		start := i * int(blockSize)
		end := start + int(blockSize)
		if end > len(data) {
			end = len(data)
		}
		block := data[start:end]

		if Checksum1(block) != bs.Sum1 {
			t.Errorf("block %d Sum1 mismatch", i)
		}
	}
}

func TestCalculateBlockSizeBoundary(t *testing.T) {
	tests := []struct {
		fileSize int64
		want     int32
	}{
		{0, 700},
		{1, 700},
		{100, 700},
		{490 * 1024, 700},       // still in <=490KB range
		{490*1024 + 1, 700},     // just above, still 700 (clamped up)
		{7_000_000, 700},        // 7000000/10000=700, clamp to 700
		{7_000_001, 700},        // 700, clamped
		{10_000_000, 1000},      // 10000000/10000=1000
		{100_000_000, 10000},    // 100000000/10000=10000
		{1_310_720_000, 131072}, // exactly max
		{2_000_000_000, 131072}, // above max, clamped
	}
	for _, tt := range tests {
		got := CalculateBlockSize(tt.fileSize)
		if got != tt.want {
			t.Errorf("CalculateBlockSize(%d) = %d, want %d", tt.fileSize, got, tt.want)
		}
	}
}

func TestGenerateSignatureParallel(t *testing.T) {
	data := make([]byte, 500*1024) // 500KB — enough blocks to split
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(len(data)))

	// Serial baseline
	serial := GenerateSignature(data, blockSize, "md5")

	// Parallel
	parallel, _ := GenerateSignatureParallel(data, blockSize, "md5")

	if serial.BlockSize != parallel.BlockSize || serial.FileSize != parallel.FileSize {
		t.Fatalf("header mismatch: serial=%+v parallel=%+v", serial, parallel)
	}
	if len(serial.BlockSums) != len(parallel.BlockSums) {
		t.Fatalf("block count: serial=%d parallel=%d", len(serial.BlockSums), len(parallel.BlockSums))
	}

	for i := range serial.BlockSums {
		sa, pa := serial.BlockSums[i], parallel.BlockSums[i]
		if sa.Index != pa.Index || sa.Sum1 != pa.Sum1 || sa.Offset != pa.Offset || sa.Length != pa.Length {
			t.Errorf("block %d mismatch:\n  serial: idx=%d sum1=%d off=%d len=%d\n  parallel: idx=%d sum1=%d off=%d len=%d",
				i, sa.Index, sa.Sum1, sa.Offset, sa.Length,
				pa.Index, pa.Sum1, pa.Offset, pa.Length)
		}
		if !bytes.Equal(sa.Sum2, pa.Sum2) {
			t.Errorf("block %d Sum2 mismatch", i)
		}
	}
}

// TestGenerateSignatureBoundary tests edge cases where the last block is
// partial AND numBlocks is exactly divisible by the SIMD batch size.
// This triggered the "total bytes" and "Length" bugs in GenerateSignatureReader.
func TestGenerateSignatureBoundary(t *testing.T) {
	blockSize := int32(700)

	// Test cases: fileSize where numBlocks % batchSize == 0 with partial last block.
	// batch sizes: AVX-512=16, AVX2=8, NEON=4.
	tests := []struct {
		name     string
		fileSize int64
	}{
		// numBlocks = ceil(size/700). Target: numBlocks mod batch == 0.
		{"8 blocks, last partial", 8*700 - 1},   // numBlocks=8 (=8),  last=699B
		{"8 blocks, last tiny", 8*700 - 690},    // numBlocks=8 (=8),  last=10B
		{"16 blocks, last partial", 16*700 - 1}, // numBlocks=16 (=16), last=699B
		{"4 blocks, last partial", 4*700 - 1},   // numBlocks=4 (=4),  last=699B
		{"not divisible", 8*700 + 300},          // numBlocks=9, last=300B — scalar path
		{"exact multiple", 8 * 700},             // numBlocks=8, all full — SIMD path
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, tt.fileSize)
			for i := range data {
				data[i] = byte((i * 7) % 251) // deterministic, not rand
			}

			serial := GenerateSignature(data, blockSize, "md5")
			parallel, _ := GenerateSignatureParallel(data, blockSize, "md5")

			if len(serial.BlockSums) != len(parallel.BlockSums) {
				t.Fatalf("block count: serial=%d parallel=%d",
					len(serial.BlockSums), len(parallel.BlockSums))
			}

			for i := range serial.BlockSums {
				sa, pa := serial.BlockSums[i], parallel.BlockSums[i]
				if sa.Index != pa.Index || sa.Sum1 != pa.Sum1 || sa.Offset != pa.Offset || sa.Length != pa.Length {
					t.Errorf("block %d mismatch:\n  serial: idx=%d sum1=%d off=%d len=%d\n  parallel: idx=%d sum1=%d off=%d len=%d",
						i, sa.Index, sa.Sum1, sa.Offset, sa.Length,
						pa.Index, pa.Sum1, pa.Offset, pa.Length)
				}
				if !bytes.Equal(sa.Sum2, pa.Sum2) {
					t.Errorf("block %d Sum2 mismatch", i)
				}
			}
		})
	}
}
func TestDeltaZeroByteFiles(t *testing.T) {
	// 0→0: should produce 0 instructions
	insts, _ := Delta([]byte{}, []byte{}, 700, "md5")
	if len(insts) != 0 {
		t.Errorf("0→0: expected 0 instructions, got %d", len(insts))
	}

	// 0→N: all literals
	newF := []byte("hello")
	insts, _ = Delta([]byte{}, newF, 700, "md5")
	literals := 0
	for _, inst := range insts {
		if inst.IsLiteral {
			literals += len(inst.Data)
		}
	}
	if literals != len(newF) {
		t.Errorf("0→N: expected %d literal bytes, got %d", len(newF), literals)
	}

	// N→0: should produce empty result
	oldF := []byte("hello")
	insts, _ = Delta(oldF, []byte{}, 700, "md5")
	result, err := ApplyDelta(oldF, insts, 700, "md5")
	if err != nil {
		t.Fatalf("N→0: ApplyDelta: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("N→0: expected 0 bytes, got %d", len(result))
	}
}

func TestDeltaTinyFiles(t *testing.T) {
	// Files smaller than blockSize → all should roundtrip
	blockSize := int32(700)
	cases := []struct {
		name    string
		oldSize int
		newSize int
	}{
		{"1→1 identical", 1, 1},
		{"1→2 extended", 1, 2},
		{"2→1 truncated", 2, 1},
		{"700→700 full block", 700, 700},
		{"699→699 partial block", 699, 699},
		{"1→1000 small→large", 1, 1000},
		{"1000→1 large→small", 1000, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldF := make([]byte, c.oldSize)
			newF := make([]byte, c.newSize)
			for i := range oldF {
				oldF[i] = byte(i * 13 % 251)
			}
			for i := range newF {
				newF[i] = byte(i * 17 % 251)
			}

			result, err := RoundTrip(oldF, newF, blockSize, "md5")
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if !bytes.Equal(result, newF) {
				t.Errorf("roundtrip mismatch: old=%d new=%d result=%d",
					c.oldSize, c.newSize, len(result))
			}
		})
	}
}

func TestDeltaRoundTrip(t *testing.T) {
	// Simulate: basisFile (old version) → newFile (new version)
	basisFile := make([]byte, 100*1024) // 100KB
	rand.Read(basisFile)

	newFile := make([]byte, 0, 100*1024+1024)
	newFile = append(newFile, basisFile[:50*1024]...)               // first half: unchanged
	newFile = append(newFile, []byte("INSERTED DATA AT MIDDLE")...) // inserted data
	newFile = append(newFile, basisFile[50*1024:]...)               // second half: unchanged

	blockSize := CalculateBlockSize(int64(len(basisFile)))

	sig := GenerateSignature(basisFile, blockSize, "md5")

	engine, _ := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	recon, _ := NewReconstructor(basisFile, blockSize, "md5")
	result, err := recon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("reconstruct failed: %v", err)
	}

	// 4. verify
	if !bytes.Equal(result, newFile) {
		t.Errorf("reconstructed file differs from original")
		t.Logf("original size: %d, reconstructed size: %d", len(newFile), len(result))
	}

	literalBytes := engine.LiteralBytes
	totalBytes := int64(len(newFile))
	savedPct := float64(totalBytes-literalBytes) / float64(totalBytes) * 100

	t.Logf("file size: %d bytes", totalBytes)
	t.Logf("block size: %d bytes", blockSize)
	t.Logf("literal data transferred: %d bytes", literalBytes)
	t.Logf("saved: %.1f%%", savedPct)
	t.Logf("matches: %d, hash hits: %d, false alarms: %d",
		engine.Matches, engine.HashHits, engine.FalseAlarms)
}

func TestDeltaIdentical(t *testing.T) {

	data := make([]byte, 50*1024)
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(len(data)))

	sig := GenerateSignature(data, blockSize, "md5")

	engine, _ := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(data)

	recon, _ := NewReconstructor(data, blockSize, "md5")
	result, err := recon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("reconstruct failed: %v", err)
	}

	if !bytes.Equal(result, data) {
		t.Error("identical file reconstructed incorrectly")
	}

	// identical files should have near-zero literal transfer
	t.Logf("identical file: literal transferred %d / %d bytes (%.2f%%)",
		engine.LiteralBytes, len(data),
		float64(engine.LiteralBytes)/float64(len(data))*100)
}

// TestDeltaIdenticalZeroLiteral verifies that matching a file against
// itself produces zero literal bytes (100% block match).  This catches
// the partial-last-block bug where the final incomplete block was never
// checked against the signature.
func TestDeltaIdenticalZeroLiteral(t *testing.T) {
	// Sizes that produce a non-zero remainder with CalculateBlockSize:
	// blockSize=700 for files <= 490KB.
	sizes := []int{
		700,        // exactly 1 block
		701,        // 1 full + 1 byte tail
		1400,       // exactly 2 blocks
		3367,       // 4 full + 567 tail (the original bug case)
		10000,      // 14 full + 200 tail
		50 * 1024,  // ~73 full + partial tail
		490 * 1024, // max file size for blockSize=700
	}
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		blockSize := CalculateBlockSize(int64(sz))

		sig := GenerateSignature(data, blockSize, "md5")
		eng, _ := NewMatchEngine(blockSize, "md5")
		eng.LoadSignature(sig)
		_ = eng.Search(data)

		if eng.LiteralBytes > 0 {
			t.Errorf("size=%d blockSize=%d: LiteralBytes=%d, expected 0 (identical file should 100%% match)",
				sz, blockSize, eng.LiteralBytes)
		}
	}
}

func TestReconstructNegativeBlockIdx(t *testing.T) {
	// Negative block index from corrupt wire data must return an error, not panic.
	// 负 BlockIdx（来自损坏的 wire 数据）必须返回错误而非 panic。
	basis := make([]byte, 1024)
	recon, _ := NewReconstructor(basis, 512, "md5")

	// Reconstruct
	_, err := recon.Reconstruct([]MatchResult{
		{IsLiteral: false, BlockIdx: -1},
	})
	if err == nil {
		t.Fatal("expected error for negative BlockIdx in Reconstruct, got nil")
	}

	// WriteInstruction
	var buf bytes.Buffer
	err = recon.WriteInstruction(&buf, MatchResult{IsLiteral: false, BlockIdx: -1})
	if err == nil {
		t.Fatal("expected error for negative BlockIdx in WriteInstruction, got nil")
	}
}

func BenchmarkSignature(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))

	b.ResetTimer()
	for b.Loop() {
		GenerateSignature(data, blockSize, "md5")
	}
}

// BenchmarkSignature_NEON measures end-to-end signature generation on the
// ARM64 NEON 4-way path (skips elsewhere). The NEON engine lives in hashsimd;
// this exercises the core dispatch end-to-end.
func BenchmarkSignature_NEON(b *testing.B) {
	if !hashsimd.MD5x4Available() {
		b.Skip("NEON not available")
	}
	const fileSize = 16 * 1024 * 1024 // 16 MB
	data := make([]byte, fileSize)
	for i := range data {
		data[i] = byte(i % 256)
	}
	blockSize := CalculateBlockSize(int64(fileSize))

	b.SetBytes(fileSize)
	b.ResetTimer()
	for b.Loop() {
		GenerateSignature(data, blockSize, "md5")
	}
}

func BenchmarkSignatureSHA256(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSignature(data, blockSize, "sha256")
	}
}

func BenchmarkSignatureXXH64(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSignature(data, blockSize, "xxh64")
	}
}

func BenchmarkSignatureXXH3(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSignature(data, blockSize, "xxh3")
	}
}

// BenchmarkSignatureReader measures the streaming reader path (used by shuttle).
// Tests md5 with AVX2 across typical shuttle block sizes.
func BenchmarkSignatureReader(b *testing.B) {
	sizes := []struct {
		name string
		data int64
		bs   int32
	}{
		{"10MB_700B", 10 * 1024 * 1024, 700},
		{"10MB_32KB", 10 * 1024 * 1024, 32 * 1024},
		{"10MB_128KB", 10 * 1024 * 1024, 128 * 1024},
		{"100MB_700B", 100 * 1024 * 1024, 700},
		{"100MB_128KB", 100 * 1024 * 1024, 128 * 1024},
	}
	for _, sz := range sizes {
		data := make([]byte, sz.data)
		rand.Read(data)
		b.Run(sz.name, func(b *testing.B) {
			b.SetBytes(sz.data)
			b.ReportAllocs()
			for b.Loop() {
				r := bytes.NewReader(data)
				GenerateSignatureReader(r, sz.data, sz.bs, "md5")
			}
		})
	}
}

func BenchmarkSearch(b *testing.B) {
	basis := make([]byte, 1024*1024) // 1MB
	rand.Read(basis)
	newFile := make([]byte, len(basis))
	copy(newFile, basis)

	for i := 0; i < len(newFile)/10; i++ {
		newFile[i*10] ^= 0xFF
	}

	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	b.ResetTimer()
	for b.Loop() {
		engine, _ := NewMatchEngine(blockSize, "md5")
		engine.LoadSignature(sig)
		engine.Search(newFile)
	}
}

// BenchmarkSearchMiss measures the worst-case search path: newFile is
// unrelated to basis, so no matches are found and every byte pays a rolling
// update + hash + random hash-table probe. Unlike BenchmarkSearch's 90%-match
// case (matched blocks jump by blockSize), every byte here is scanned.
// At 1MB the hash table (min 65536 entries = 512KB) is L2-resident on Zen 4
// (~318 MB/s); the L3-resident worst case for large files is ~100-130 MB/s.
func BenchmarkSearchMiss(b *testing.B) {
	basis := make([]byte, 1<<20)
	rand.Read(basis)
	newFile := make([]byte, 1<<20)
	rand.Read(newFile) // unrelated → all miss

	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	b.ResetTimer()
	for b.Loop() {
		engine, _ := NewMatchEngine(blockSize, "md5")
		engine.LoadSignature(sig)
		engine.Search(newFile)
	}
}

// BenchmarkSearchMatrix sweeps file size (block size) and match density:
//
//	miss:      unrelated newFile → no matches (slowest per byte)
//	match90:   10% of bytes modified (typical changed file)
//	identical: byte-for-byte copy → every block matches (fastest)
//
// 1MB → blockSize 700 (AVX2 MD5); 32MB → blockSize ~2KB (AVX-512 MD5 path
// in signature generation). Hash tables stay L2-resident at these sizes.
func BenchmarkSearchMatrix(b *testing.B) {
	sizes := []int{1 << 20, 32 << 20}
	for _, size := range sizes {
		basis := make([]byte, size)
		rand.Read(basis)
		miss := make([]byte, size)
		rand.Read(miss)
		match90 := make([]byte, size)
		copy(match90, basis)
		for i := 0; i < len(match90)/10; i++ {
			match90[i*10] ^= 0xFF
		}
		identical := make([]byte, size)
		copy(identical, basis)

		blockSize := CalculateBlockSize(int64(size))
		sig := GenerateSignature(basis, blockSize, "md5")

		label := fmt.Sprintf("%dMB", size>>20)
		files := map[string][]byte{"miss": miss, "match90": match90, "identical": identical}
		for _, name := range []string{"miss", "match90", "identical"} {
			nf := files[name]
			b.Run(label+"/"+name, func(b *testing.B) {
				b.SetBytes(int64(size))
				for b.Loop() {
					engine, _ := NewMatchEngine(blockSize, "md5")
					engine.LoadSignature(sig)
					engine.Search(nf)
				}
			})
		}
	}
}

// BenchmarkRollValue measures the rolling-sum hot-path arithmetic in
// isolation. Roll + Value form a serial dependency chain (each Roll depends
// on the previous state) — this is what the search loop pays per byte before
// the hash-table lookup. Constant bytes isolate arithmetic from memory;
// blockSize is fixed at 700 (the CalculateBlockSize default for 1MB files).
// Useful as a baseline for any future Roll optimization.
func BenchmarkRollValue(b *testing.B) {
	blockSize := int32(700)
	data := make([]byte, blockSize+1)
	rand.Read(data)
	rs := NewRollingSum(data[:blockSize])

	b.Run("RollOnly", func(b *testing.B) {
		var sink uint32
		for b.Loop() {
			rs.Roll(data[0], data[blockSize], blockSize)
			sink += rs.s1
		}
		_ = sink
	})
	b.Run("RollAndValue", func(b *testing.B) {
		var sink uint32
		for b.Loop() {
			rs.Roll(data[0], data[blockSize], blockSize)
			sink += rs.Value()
		}
		_ = sink
	})
}

func BenchmarkChecksum1(b *testing.B) {
	sizes := []int{1024, 8192, 65536, 1048576}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		b.Run(fmt.Sprintf("%dKB", size/1024), func(b *testing.B) {
			b.SetBytes(int64(size))
			for b.Loop() {
				Checksum1(data)
			}
		})
	}
}

func TestExampleUsage(t *testing.T) {

	oldFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is an example of delta transfer.")
	// new file (with insertion in the middle)
	newFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"INSERTED CONTENT HERE. " +
		"This is an example of delta transfer.")

	blockSize := int32(32)

	// 1. generate signature for old file
	sig := GenerateSignature(oldFile, blockSize, "md5")

	engine, _ := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	// 3. reconstruct
	recon, _ := NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(instructions)

	t.Logf("original: %s", newFile)
	t.Logf("reconstructed: %s", result)
	t.Logf("match: %v", bytes.Equal(result, newFile))
	t.Logf("transfer ratio: %.0f%%",
		float64(engine.LiteralBytes)/float64(len(newFile))*100)
}

// TestSpeedComparison benchmarks signature generation and search speed
func TestSpeedComparison(t *testing.T) {
	fileSize := 10 * 1024 * 1024 // 10MB
	data := make([]byte, fileSize)
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(fileSize))

	// signature generation speed
	start := time.Now()
	sig := GenerateSignature(data, blockSize, "md5")
	sigTime := time.Since(start)
	t.Logf("signature generation: %v (%.1f MB/s)", sigTime,
		float64(fileSize)/1024/1024/sigTime.Seconds())

	modified := make([]byte, fileSize)
	copy(modified, data)
	for i := 0; i < fileSize/20; i++ {
		modified[i*20] ^= 0xFF
	}

	engine, _ := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)

	start = time.Now()
	instructions := engine.Search(modified)
	searchTime := time.Since(start)
	t.Logf("search: %v (%.1f MB/s)", searchTime,
		float64(fileSize)/1024/1024/searchTime.Seconds())
	t.Logf("instructions: %d, literal data: %d bytes (%.1f%%)",
		len(instructions), engine.LiteralBytes,
		float64(engine.LiteralBytes)/float64(fileSize)*100)
}

// ── Streaming SearchReader tests ────────────────────────────────────────

func TestSearchReaderParity(t *testing.T) {
	// Verify SearchReader produces identical results to Search.
	sizes := []int{1024, 10 * 1024, 100 * 1024}
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		blockSize := CalculateBlockSize(int64(sz))

		// Generate signature from data (old file = new file for parity)
		sig := GenerateSignature(data, blockSize, "md5")

		// Batch search
		eng1, _ := NewMatchEngine(blockSize, "md5")
		eng1.LoadSignature(sig)
		batchResults := eng1.Search(data)

		// Streaming search
		eng2, _ := NewMatchEngine(blockSize, "md5")
		eng2.LoadSignature(sig)
		var streamResults []MatchResult
		err := eng2.SearchReader(bytes.NewReader(data), int64(len(data)), func(mr MatchResult) error {
			// Copy Data since it's only valid during callback
			mrCopy := mr
			if mr.IsLiteral {
				mrCopy.Data = make([]byte, len(mr.Data))
				copy(mrCopy.Data, mr.Data)
			}
			streamResults = append(streamResults, mrCopy)
			return nil
		})
		if err != nil {
			t.Fatalf("SearchReader error: %v", err)
		}

		// Compare
		if len(batchResults) != len(streamResults) {
			t.Errorf("size=%d: batch %d results, stream %d results", sz, len(batchResults), len(streamResults))
			continue
		}
		for i := range batchResults {
			if batchResults[i].IsLiteral != streamResults[i].IsLiteral {
				t.Errorf("size=%d result[%d]: batch.IsLiteral=%v stream.IsLiteral=%v", sz, i, batchResults[i].IsLiteral, streamResults[i].IsLiteral)
			}
			if !batchResults[i].IsLiteral && batchResults[i].BlockIdx != streamResults[i].BlockIdx {
				t.Errorf("size=%d result[%d]: batch.BlockIdx=%d stream.BlockIdx=%d", sz, i, batchResults[i].BlockIdx, streamResults[i].BlockIdx)
			}
			if batchResults[i].IsLiteral && !bytes.Equal(batchResults[i].Data, streamResults[i].Data) {
				t.Errorf("size=%d result[%d]: literal data mismatch (len batch=%d stream=%d)", sz, i, len(batchResults[i].Data), len(streamResults[i].Data))
			}
		}

		// Stats should match
		if eng1.Matches != eng2.Matches {
			t.Errorf("size=%d: Matches batch=%d stream=%d", sz, eng1.Matches, eng2.Matches)
		}
		if eng1.LiteralBytes != eng2.LiteralBytes {
			t.Errorf("size=%d: LiteralBytes batch=%d stream=%d", sz, eng1.LiteralBytes, eng2.LiteralBytes)
		}
	}
}

func TestSearchReaderSmallFile(t *testing.T) {
	// File smaller than blockSize: should emit as single literal.
	data := []byte("hello world")
	blockSize := int32(700)

	sig := GenerateSignature(data, blockSize, "md5")
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)

	var results []MatchResult
	err := eng.SearchReader(bytes.NewReader(data), int64(len(data)), func(mr MatchResult) error {
		mrCopy := mr
		if mr.IsLiteral {
			mrCopy.Data = make([]byte, len(mr.Data))
			copy(mrCopy.Data, mr.Data)
		}
		results = append(results, mrCopy)
		return nil
	})
	if err != nil {
		t.Fatalf("SearchReader error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsLiteral {
		t.Error("expected literal result")
	}
	if !bytes.Equal(results[0].Data, data) {
		t.Errorf("data mismatch: got %q, want %q", results[0].Data, data)
	}
}

func TestSearchReaderRoundTrip(t *testing.T) {
	// Full streaming roundtrip: DeltaStream → collect instructions → ApplyDelta.
	basisFile := make([]byte, 100*1024)
	rand.Read(basisFile)

	newFile := make([]byte, 0, 100*1024+1024)
	newFile = append(newFile, basisFile[:50*1024]...)
	newFile = append(newFile, []byte("INSERTED DATA AT MIDDLE")...)
	newFile = append(newFile, basisFile[50*1024:]...)

	blockSize := CalculateBlockSize(int64(len(basisFile)))

	// Streaming sender side: collect instructions via DeltaStream.
	var instructions []MatchResult
	err := DeltaStream(basisFile, bytes.NewReader(newFile), int64(len(newFile)), blockSize, "md5",
		func(mr MatchResult) error {
			// Copy Data since it's only valid during callback.
			mrCopy := mr
			if mr.IsLiteral {
				mrCopy.Data = make([]byte, len(mr.Data))
				copy(mrCopy.Data, mr.Data)
			}
			instructions = append(instructions, mrCopy)
			return nil
		})
	if err != nil {
		t.Fatalf("DeltaStream: %v", err)
	}

	// Reconstruct from collected instructions.
	result, err := ApplyDelta(basisFile, instructions, blockSize, "md5")
	if err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}

	if !bytes.Equal(result, newFile) {
		t.Errorf("streaming roundtrip mismatch")
		t.Logf("original: %d bytes, reconstructed: %d bytes", len(newFile), len(result))
		// Find first differing byte.
		for i := 0; i < len(result) && i < len(newFile); i++ {
			if result[i] != newFile[i] {
				t.Logf("first diff at byte %d: got %d, want %d", i, result[i], newFile[i])
				break
			}
		}
	}

	// Also verify wire encoding/decoding roundtrip with streaming.
	var wireBuf bytes.Buffer
	if err := WireEncodeInstructions(&wireBuf, instructions); err != nil {
		t.Fatalf("WireEncodeInstructions: %v", err)
	}

	var reconBuf bytes.Buffer
	if err := ApplyDeltaStream(basisFile, &wireBuf, &reconBuf, blockSize, "md5"); err != nil {
		t.Fatalf("ApplyDeltaStream: %v", err)
	}
	if !bytes.Equal(reconBuf.Bytes(), newFile) {
		t.Errorf("wire streaming roundtrip mismatch: got %d bytes, want %d bytes", reconBuf.Len(), len(newFile))
	}
}

func TestSearchReaderLiteralFlush(t *testing.T) {
	// Test that literal backlog is correctly flushed when no matches exist.
	// Two completely different files → all literal output.
	basisFile := make([]byte, 50*1024)
	rand.Read(basisFile)

	newFile := make([]byte, 50*1024)
	rand.Read(newFile) // different random data → no matches

	blockSize := CalculateBlockSize(int64(len(basisFile)))
	sig := GenerateSignature(basisFile, blockSize, "md5")

	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)

	var totalLiteral int64
	var chunks int
	err := eng.SearchReader(bytes.NewReader(newFile), int64(len(newFile)), func(mr MatchResult) error {
		if mr.IsLiteral {
			totalLiteral += int64(len(mr.Data))
			chunks++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SearchReader: %v", err)
	}

	if totalLiteral != int64(len(newFile)) {
		t.Errorf("expected all %d bytes as literal, got %d", len(newFile), totalLiteral)
	}
	t.Logf("literal chunks: %d, total literal: %d bytes", chunks, totalLiteral)
}

func BenchmarkSearchReader(b *testing.B) {
	// Same data pattern as BenchmarkSearch for fair comparison.
	basis := make([]byte, 1024*1024) // 1MB
	rand.Read(basis)
	newFile := make([]byte, len(basis))
	copy(newFile, basis)
	for i := 0; i < len(newFile)/10; i++ {
		newFile[i*10] ^= 0xFF // 10% bytes modified
	}

	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		eng, _ := NewMatchEngine(blockSize, "md5")
		eng.LoadSignature(sig)
		eng.SearchReader(bytes.NewReader(newFile), int64(len(newFile)), func(MatchResult) error {
			return nil
		})
	}
}

func BenchmarkSearchReaderParallel(b *testing.B) {
	const size = 32 << 20
	basis := make([]byte, size)
	rand.Read(basis)
	newFile := make([]byte, size)
	rand.Read(newFile) // unrelated → all-miss
	blockSize := CalculateBlockSize(size)
	sig := GenerateSignature(basis, blockSize, "md5")

	for _, window := range []int{8 << 20, 32 << 20} {
		b.Run(fmt.Sprintf("32MB_window%dMB", window>>20), func(b *testing.B) {
			b.SetBytes(size)
			b.ReportAllocs()
			for b.Loop() {
				eng, _ := NewMatchEngine(blockSize, "md5")
				eng.LoadSignature(sig)
				eng.SearchReaderParallel(bytes.NewReader(newFile), size, window, 0, func(MatchResult) error {
					return nil
				})
			}
		})
	}
}

// ── Parallel Search tests ──────────────────────────────────────────────

func TestSearchParallelParity(t *testing.T) {
	// Verify SearchParallel produces same results as Search.
	sizes := []int{10 * 1024, 100 * 1024, 500 * 1024}
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		blockSize := CalculateBlockSize(int64(sz))

		// Modify ~10% to create non-trivial delta
		modified := make([]byte, sz)
		copy(modified, data)
		for i := 0; i < len(modified)/10; i++ {
			modified[i*10] ^= 0xFF
		}

		sig := GenerateSignature(data, blockSize, "md5")

		// Serial
		eng1, _ := NewMatchEngine(blockSize, "md5")
		eng1.LoadSignature(sig)
		serial := eng1.Search(modified)

		// Parallel (2, 4, 8 workers)
		for _, workers := range []int{2, 4, 8} {
			eng2, _ := NewMatchEngine(blockSize, "md5")
			eng2.LoadSignature(sig)
			parallel := eng2.SearchParallel(modified, workers)

			// Reconstruct and verify
			recon, _ := NewReconstructor(data, blockSize, "md5")
			serialResult, _ := recon.Reconstruct(serial)
			parallelResult, _ := recon.Reconstruct(parallel)

			if !bytes.Equal(serialResult, parallelResult) {
				t.Errorf("size=%d workers=%d: serial and parallel produce different output", sz, workers)
				t.Logf("serial results: %d, parallel results: %d", len(serial), len(parallel))
				t.Logf("serial len: %d, parallel len: %d", len(serialResult), len(parallelResult))
				// Find first difference
				for i := 0; i < len(serialResult) && i < len(parallelResult); i++ {
					if serialResult[i] != parallelResult[i] {
						t.Logf("first diff at byte %d", i)
						break
					}
				}
			}
		}
	}
}

// TestSearchParallelBoundaryCrossingMatch verifies that a match which starts
// before a segment boundary and extends past it cannot make the next segment
// re-emit bytes already covered by that match.  The parallel result must
// reconstruct to exactly the same file as the serial result.
func TestSearchParallelBoundaryCrossingMatch(t *testing.T) {
	const size = 2 << 20
	basis := make([]byte, size)
	rand.Read(basis)
	blockSize := CalculateBlockSize(size)
	sig := GenerateSignature(basis, blockSize, "md5")

	// Put a clean run of basis data starting half a block before the first
	// segment boundary, so the first match deliberately crosses the boundary.
	chunk := ((size+1)/2 + int(blockSize) - 1) / int(blockSize) * int(blockSize)
	segEnd := chunk
	first := segEnd - int(blockSize)/2
	newFile := make([]byte, size)
	rand.Read(newFile[:first])
	copy(newFile[first:], basis[:size-first])

	// Serial baseline must reconstruct correctly first.
	eng1, _ := NewMatchEngine(blockSize, "md5")
	eng1.LoadSignature(sig)
	serial := eng1.Search(newFile)
	recon1, _ := NewReconstructor(basis, blockSize, "md5")
	out1, err := recon1.Reconstruct(serial)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1, newFile) {
		t.Fatal("serial baseline reconstruction mismatch")
	}

	for _, workers := range []int{2, 3, 4, 8} {
		eng2, _ := NewMatchEngine(blockSize, "md5")
		eng2.LoadSignature(sig)
		parallel := eng2.SearchParallel(newFile, workers)
		recon2, _ := NewReconstructor(basis, blockSize, "md5")
		out2, err := recon2.Reconstruct(parallel)
		if err != nil {
			t.Fatalf("workers=%d: reconstruct: %v", workers, err)
		}
		if !bytes.Equal(out2, newFile) {
			t.Fatalf("workers=%d: parallel reconstruction mismatch (len=%d want=%d)",
				workers, len(out2), len(newFile))
		}
	}
}

func TestSearchReaderParallelParity(t *testing.T) {
	sizes := []int{1<<20 + 123, 2<<20 + 4567}
	windows := []int{1 << 17, 333331}
	for _, sz := range sizes {
		basis := make([]byte, sz)
		rand.Read(basis)
		newFile := make([]byte, sz)
		copy(newFile, basis)
		// Modify a few regions so the delta is non-trivial.
		for i := 0; i < sz/7; i++ {
			newFile[i*7] ^= 0xFF
		}
		blockSize := CalculateBlockSize(int64(sz))
		sig := GenerateSignature(basis, blockSize, "md5")

		// Serial baseline.
		eng1, _ := NewMatchEngine(blockSize, "md5")
		eng1.LoadSignature(sig)
		serial := eng1.Search(newFile)
		recon1, _ := NewReconstructor(basis, blockSize, "md5")
		out1, err := recon1.Reconstruct(serial)
		if err != nil {
			t.Fatal(err)
		}

		for _, window := range windows {
			eng2, _ := NewMatchEngine(blockSize, "md5")
			eng2.LoadSignature(sig)
			var parallel []MatchResult
			err := eng2.SearchReaderParallel(bytes.NewReader(newFile), int64(sz), window, 0, func(mr MatchResult) error {
				cp := mr
				if mr.IsLiteral {
					cp.Data = append([]byte(nil), mr.Data...)
				}
				parallel = append(parallel, cp)
				return nil
			})
			if err != nil {
				t.Fatalf("size=%d window=%d: SearchReaderParallel: %v", sz, window, err)
			}

			recon2, _ := NewReconstructor(basis, blockSize, "md5")
			out2, err := recon2.Reconstruct(parallel)
			if err != nil {
				t.Fatalf("size=%d window=%d: reconstruct: %v", sz, window, err)
			}
			if !bytes.Equal(out2, out1) {
				t.Fatalf("size=%d window=%d: parallel streaming output differs from serial (len=%d want=%d)",
					sz, window, len(out2), len(out1))
			}
			if !bytes.Equal(out2, newFile) {
				t.Fatalf("size=%d window=%d: parallel streaming output != newFile", sz, window)
			}
		}
	}
}

// TestSearchReaderParallelWindowBoundary covers a match phase that would
// cross a streaming window boundary; the two windows must still reconstruct
// byte-for-byte without duplicating the covered range.
func TestSearchReaderParallelWindowBoundary(t *testing.T) {
	const size = 2 << 20
	basis := make([]byte, size)
	rand.Read(basis)
	blockSize := CalculateBlockSize(size)
	sig := GenerateSignature(basis, blockSize, "md5")

	const window = 137777 // deliberately not block-aligned
	first := window - int(blockSize)/2
	newFile := make([]byte, size)
	rand.Read(newFile[:first])
	copy(newFile[first:], basis[:size-first])

	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	var results []MatchResult
	err := eng.SearchReaderParallel(bytes.NewReader(newFile), size, window, 0, func(mr MatchResult) error {
		cp := mr
		if mr.IsLiteral {
			cp.Data = append([]byte(nil), mr.Data...)
		}
		results = append(results, cp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recon, _ := NewReconstructor(basis, blockSize, "md5")
	out, err := recon.Reconstruct(results)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, newFile) {
		t.Fatalf("window-boundary reconstruction mismatch (len=%d want=%d)", len(out), len(newFile))
	}
}

func TestSearchParallelIdentical(t *testing.T) {
	// Identical files should produce near-zero literals with parallel.
	data := make([]byte, 200*1024)
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))

	sig := GenerateSignature(data, blockSize, "md5")

	// Serial baseline
	engSer, _ := NewMatchEngine(blockSize, "md5")
	engSer.LoadSignature(sig)
	serial := engSer.Search(data)

	// Parallel
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	parallel := eng.SearchParallel(data, 4)

	// Reconstruct both
	recon, _ := NewReconstructor(data, blockSize, "md5")
	serialResult, _ := recon.Reconstruct(serial)
	parallelResult, _ := recon.Reconstruct(parallel)

	if !bytes.Equal(serialResult, parallelResult) {
		t.Error("parallel identical file reconstructed differently from serial")
		t.Logf("serial result: %d bytes, %d instructions", len(serialResult), len(serial))
		t.Logf("parallel result: %d bytes, %d instructions", len(parallelResult), len(parallel))
		// Find first diff.
		for i := 0; i < len(serialResult) && i < len(parallelResult); i++ {
			if serialResult[i] != parallelResult[i] {
				t.Logf("first diff at byte %d: serial=%d parallel=%d", i, serialResult[i], parallelResult[i])
				break
			}
		}
		// Compare instructions.
		for i := 0; i < len(serial) && i < len(parallel); i++ {
			s, p := serial[i], parallel[i]
			if s.IsLiteral != p.IsLiteral || (!s.IsLiteral && s.BlockIdx != p.BlockIdx) ||
				(s.IsLiteral && !bytes.Equal(s.Data, p.Data)) {
				t.Logf("instruction[%d] differs: serial={lit=%v idx=%d off=%d len=%d} parallel={lit=%v idx=%d off=%d len=%d}",
					i, s.IsLiteral, s.BlockIdx, s.Offset, len(s.Data),
					p.IsLiteral, p.BlockIdx, p.Offset, len(p.Data))
				break
			}
		}
	}
	if !bytes.Equal(parallelResult, data) {
		t.Error("parallel identical file reconstructed incorrectly")
	}
	t.Logf("workers=4, literal: %d/%d bytes (%.2f%%)",
		eng.LiteralBytes, len(data), float64(eng.LiteralBytes)/float64(len(data))*100)
}

func TestSearchParallelSmallFile(t *testing.T) {
	// File smaller than blockSize: should fall back to serial.
	data := []byte("hello parallel world")
	blockSize := int32(700)

	sig := GenerateSignature(data, blockSize, "md5")
	eng, _ := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)

	serial := eng.Search(data)
	parallel := eng.SearchParallel(data, 4)

	if len(serial) != len(parallel) {
		t.Errorf("serial=%d results, parallel=%d", len(serial), len(parallel))
	}
	for i := range serial {
		if serial[i].IsLiteral != parallel[i].IsLiteral {
			t.Errorf("result[%d] mismatch", i)
		}
	}
}

func BenchmarkSearchParallel(b *testing.B) {
	basis := make([]byte, 1024*1024) // 1MB
	rand.Read(basis)
	newFile := make([]byte, len(basis))
	copy(newFile, basis)
	for i := 0; i < len(newFile)/10; i++ {
		newFile[i*10] ^= 0xFF
	}

	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	for _, workers := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				eng, _ := NewMatchEngine(blockSize, "md5")
				eng.LoadSignature(sig)
				eng.SearchParallel(newFile, workers)
			}
		})
	}
}

// TestChecksum1Parity verifies that Checksum1 (used by GenerateSignature)
// and checksum1 (used by NewRollingSum) produce identical results across
// all block sizes, including the [65536, 92681] range where the
// CHAR_OFFSET post-correction's uint32 intermediate multiplication wraps.
//
// Since v7 the packed path runs entirely in 16-bit lanes (mod 2^16); the
// checksum1 path uses 16-bit asm raw sums + a 32-bit Go post-correction.
// Both pack to the same 16-bit components via different routes:
//   - Checksum1 → checksum1PackedAVX2 (asm, 16-bit lanes, built-in CHAR_OFFSET)
//   - checksum1 → checksum1AVX2 (asm, 16-bit raw) + Go post-correction
//
// If these ever diverge, signature generation and rolling match would
// compute different weak checksums for the same block, causing match
// failures in delta search.
func TestChecksum1Parity(t *testing.T) {
	// Cover the full blockSize range, with dense sampling in the
	// overflow-prone zone [65536, 92681] where n*(n+1) ≥ 2³².
	sizes := []int{
		512, 1024, 4096, 8192, 16384, 32768,
		// Overflow zone: n*(n+1) overflows uint32
		65535, 65536, 65537, 70000, 81920, 92680, 92681, 92682,
		// Post-overflow zone: n*(n+1) wraps back into range
		100000, 128 * 1024,
	}
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)

		// Path A: Checksum1 (signature generation path)
		packed := Checksum1(data)

		// Path B: checksum1 (rolling match path), packed manually
		cs1, cs2 := checksum1(data)
		manual := (cs1 & 0xFFFF) | ((cs2 & 0xFFFF) << 16)

		if packed != manual {
			t.Errorf("n=%d: Checksum1=%08x checksum1=%08x — DIVERGENCE", n, packed, manual)
		}
	}

	// End-to-end: delta roundtrip with a blockSize in the overflow zone.
	// Use a file large enough for CalculateBlockSize to pick ≥65536.
	bigSize := 700 * 1024 * 1024 // 700 MB
	blockSize := CalculateBlockSize(int64(bigSize))
	if blockSize < 65536 || blockSize > 92681 {
		t.Skipf("CalculateBlockSize(%d) = %d, not in overflow zone; skipping roundtrip", bigSize, blockSize)
	}

	// Use small representative slices instead of 700 MB.
	// The checksum parity above already covers the blockSize;
	// here we just confirm the roundtrip pipeline doesn't break.
	oldFile := make([]byte, 2*int(blockSize))
	newFile := make([]byte, 2*int(blockSize))
	rand.Read(oldFile)
	copy(newFile, oldFile)
	// Modify a few bytes so it's not trivially identical.
	for i := int(blockSize); i < int(blockSize)+100; i++ {
		newFile[i] ^= 0xFF
	}

	result, err := RoundTrip(oldFile, newFile, blockSize, "md5")
	if err != nil {
		t.Fatalf("roundtrip at blockSize=%d: %v", blockSize, err)
	}
	if !bytes.Equal(result, newFile) {
		t.Fatalf("roundtrip at blockSize=%d: result != newFile", blockSize)
	}
}

// TestChecksum1RawVsDirect documents a known divergence: the AVX2/SSE2
// "raw sums + CHAR_OFFSET post-correction" path produces different s2
// values than byte-by-byte accumulation when blockSize ∈ [65536, 92681].
//
// This is NOT a bug: both Checksum1 and checksum1 use the same
// raw+correction path, so the delta pipeline is internally consistent.
// The divergence only matters cross-ISA (e.g. AVX2 sender + pure-Go ARM
// receiver), which go-rsync does not support.
//
// This test exists to make the divergence explicit and catch accidental
// assumptions that the two paths are byte-identical.
func TestChecksum1RawVsDirect(t *testing.T) {
	charOffset := uint32(31)

	// direct: byte-by-byte with CHAR_OFFSET (pure-Go fallback)
	direct := func(data []byte) (s1, s2 uint32) {
		for _, b := range data {
			s1 += uint32(b) + charOffset
			s2 += s1
		}
		return
	}

	// corrected: raw sums + post-correction (AVX2/SSE2 path)
	corrected := func(data []byte) (s1, s2 uint32) {
		n := len(data)
		for _, b := range data {
			s1 += uint32(b)
			s2 += s1
		}
		s1 += uint32(n) * charOffset
		s2 += uint32(n) * uint32(n+1) / 2 * charOffset
		return
	}

	sizes := []int{
		512, 4096, 16384, 32768,
		65536, 70000, 92681, // overflow zone: s2 diverges
		92682, 100000, 128 * 1024,
	}

	diverged := false
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)
		s1d, s2d := direct(data)
		s1c, s2c := corrected(data)

		s1ok := s1d == s1c
		s2ok := s2d == s2c

		if !s1ok || !s2ok {
			diverged = true
			t.Logf("n=%d: s1 %v s2 %v (expected in overflow zone)", n,
				map[bool]string{true: "ok", false: "DIVERGE"}[s1ok],
				map[bool]string{true: "ok", false: "DIVERGE"}[s2ok])
		}
	}

	if !diverged {
		t.Error("expected s2 divergence in [65536, 92681]; if this fails, " +
			"the CHAR_OFFSET correction may have been changed to use " +
			"uint64 intermediates")
	}
}
