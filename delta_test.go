package delta

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
	"time"
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
func TestGenerateSignatureParallel(t *testing.T) {
	data := make([]byte, 500*1024) // 500KB — enough blocks to split
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(len(data)))

	// Serial baseline
	serial := GenerateSignature(data, blockSize, "md5")

	// Parallel
	parallel := GenerateSignatureParallel(data, blockSize, "md5")

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

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	recon := NewReconstructor(basisFile, blockSize, "md5")
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

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(data)

	recon := NewReconstructor(data, blockSize, "md5")
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
		700,          // exactly 1 block
		701,          // 1 full + 1 byte tail
		1400,         // exactly 2 blocks
		3367,         // 4 full + 567 tail (the original bug case)
		10000,        // 14 full + 200 tail
		50 * 1024,    // ~73 full + partial tail
		490 * 1024,   // max file size for blockSize=700
	}
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		blockSize := CalculateBlockSize(int64(sz))

		sig := GenerateSignature(data, blockSize, "md5")
		eng := NewMatchEngine(blockSize, "md5")
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
	recon := NewReconstructor(basis, 512, "md5")

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
		engine := NewMatchEngine(blockSize, "md5")
		engine.LoadSignature(sig)
		engine.Search(newFile)
	}
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
		"This is an example of rsync-style delta transfer.")
	// new file (with insertion in the middle)
	newFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"INSERTED CONTENT HERE. " +
		"This is an example of rsync-style delta transfer.")

	blockSize := int32(32)

	// 1. generate signature for old file
	sig := GenerateSignature(oldFile, blockSize, "md5")

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	// 3. reconstruct
	recon := NewReconstructor(oldFile, blockSize, "md5")
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

	engine := NewMatchEngine(blockSize, "md5")
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
		eng1 := NewMatchEngine(blockSize, "md5")
		eng1.LoadSignature(sig)
		batchResults := eng1.Search(data)

		// Streaming search
		eng2 := NewMatchEngine(blockSize, "md5")
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
	eng := NewMatchEngine(blockSize, "md5")
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

	eng := NewMatchEngine(blockSize, "md5")
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
		eng := NewMatchEngine(blockSize, "md5")
		eng.LoadSignature(sig)
		eng.SearchReader(bytes.NewReader(newFile), int64(len(newFile)), func(MatchResult) error {
			return nil
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
		eng1 := NewMatchEngine(blockSize, "md5")
		eng1.LoadSignature(sig)
		serial := eng1.Search(modified)

		// Parallel (2, 4, 8 workers)
		for _, workers := range []int{2, 4, 8} {
			eng2 := NewMatchEngine(blockSize, "md5")
			eng2.LoadSignature(sig)
			parallel := eng2.SearchParallel(modified, workers)

			// Reconstruct and verify
			recon := NewReconstructor(data, blockSize, "md5")
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

func TestSearchParallelIdentical(t *testing.T) {
	// Identical files should produce near-zero literals with parallel.
	data := make([]byte, 200*1024)
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))

	sig := GenerateSignature(data, blockSize, "md5")

	// Serial baseline
	engSer := NewMatchEngine(blockSize, "md5")
	engSer.LoadSignature(sig)
	serial := engSer.Search(data)

	// Parallel
	eng := NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	parallel := eng.SearchParallel(data, 4)

	// Reconstruct both
	recon := NewReconstructor(data, blockSize, "md5")
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
	eng := NewMatchEngine(blockSize, "md5")
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
				eng := NewMatchEngine(blockSize, "md5")
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
// Both paths use 32-bit overflow intentionally (matching rsync's uint32
// arithmetic), but they reach the same result via different routes:
//   - Checksum1 → checksum1PackedAVX2 (asm, built-in CHAR_OFFSET)
//   - checksum1 → checksum1AVX2 (asm, raw sums) + Go post-correction
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
// This is NOT a bug — see docs/checksum-engine.md §5.1. Both Checksum1
// and checksum1 use the same raw+correction path, so the delta pipeline
// is internally consistent. The divergence only matters cross-ISA (e.g.
// AVX2 sender + pure-Go ARM receiver), which go-rsync does not support.
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
			"uint64 intermediates — update docs/checksum-engine.md §5.1")
	}
}
