package delta

import (
	"crypto/rand"
	"testing"
	"time"
)

func TestLargeFileSearch(t *testing.T) {
	t.Skip("manual test for large file debugging")

	// Create 100MB of random data
	size := 100 * 1024 * 1024
	data := make([]byte, size)
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(size))
	t.Logf("File: %d MB, blockSize: %d bytes, blocks: %d",
		size/(1024*1024), blockSize, (size+int(blockSize)-1)/int(blockSize))

	sig := GenerateSignature(data, blockSize, "md5")
	t.Logf("Signature: %d blocks generated", len(sig.BlockSums))

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)

	start := time.Now()
	_ = engine.Search(data)
	elapsed := time.Since(start)

	t.Logf("Search took: %v", elapsed)
	t.Logf("Matches: %d, HashHits: %d, FalseAlarms: %d",
		engine.Matches, engine.HashHits, engine.FalseAlarms)
	t.Logf("LiteralBytes: %d / %d (%.2f%%)",
		engine.LiteralBytes, size,
		float64(engine.LiteralBytes)/float64(size)*100)
	t.Logf("Search speed: %.1f MB/s",
		float64(size)/(1024*1024)/elapsed.Seconds())

	if engine.LiteralBytes > int64(blockSize)*2 {
		t.Errorf("Too many literal bytes for identical file: %d", engine.LiteralBytes)
	}
}
