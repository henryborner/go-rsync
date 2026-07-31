package delta_test

import (
	"bytes"
	"fmt"

	delta "github.com/henryborner/go-rsync"
)

// Example demonstrates a complete roundtrip: signature → match → reconstruct.
func Example() {
	oldFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is an example of rsync-style delta transfer.")
	newFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"INSERTED CONTENT HERE. " +
		"This is an example of rsync-style delta transfer.")

	blockSize := int32(32)

	sig := delta.GenerateSignature(oldFile, blockSize, "md5")
	eng, _ := delta.NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	instructions := eng.Search(newFile)

	recon, _ := delta.NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(instructions)

	fmt.Println(bytes.Equal(result, newFile))
	fmt.Printf("Transfer: %.0f%%\n",
		float64(eng.LiteralBytes)/float64(len(newFile))*100)

	// Output:
	// true
	// Transfer: 73%
}

// Example_streaming demonstrates the low-memory streaming API.
// Instead of loading the entire new file, SearchReader reads from
// an io.Reader and delivers results via callback as they are found.
func Example_streaming() {
	oldFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is an example of rsync-style delta transfer.")
	newFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"INSERTED CONTENT HERE. " +
		"This is an example of rsync-style delta transfer.")

	blockSize := int32(32)
	sig := delta.GenerateSignature(oldFile, blockSize, "md5")
	eng, _ := delta.NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)

	var instructions []delta.MatchResult
	eng.SearchReader(bytes.NewReader(newFile), int64(len(newFile)),
		func(mr delta.MatchResult) error {
			cp := mr
			if mr.IsLiteral {
				cp.Data = make([]byte, len(mr.Data))
				copy(cp.Data, mr.Data)
			}
			instructions = append(instructions, cp)
			return nil
		})

	recon, _ := delta.NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(instructions)

	fmt.Println(bytes.Equal(result, newFile))
	fmt.Printf("Transfer: %.0f%%\n",
		float64(eng.LiteralBytes)/float64(len(newFile))*100)

	// Output:
	// true
	// Transfer: 73%
}

// Example_parallel demonstrates parallel search for multi-core speedup.
func Example_parallel() {
	oldFile := make([]byte, 100*1024) // 100KB
	for i := range oldFile {
		oldFile[i] = byte(i % 256)
	}
	newFile := make([]byte, len(oldFile))
	copy(newFile, oldFile)

	blockSize := delta.CalculateBlockSize(int64(len(oldFile)))
	sig := delta.GenerateSignature(oldFile, blockSize, "md5")

	eng, _ := delta.NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	results := eng.SearchParallel(newFile, 4) // 4 workers

	recon, _ := delta.NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(results)

	fmt.Println(bytes.Equal(result, newFile))
	// For identical files, almost all bytes are matched (near-zero transfer).
	fmt.Printf("transfer: %d / %d bytes\n", eng.LiteralBytes, len(newFile))

	// Output:
	// true
	// transfer: 200 / 102400 bytes
}
