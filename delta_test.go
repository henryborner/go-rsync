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

func BenchmarkSignature(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateSignature(data, blockSize, "md5")
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
	for i := 0; i < b.N; i++ {
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
			for i := 0; i < b.N; i++ {
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
