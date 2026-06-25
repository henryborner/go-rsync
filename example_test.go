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

	// 1. Generate signature for old file
	sig := delta.GenerateSignature(oldFile, blockSize, "md5")

	// 2. Search new file for matching blocks
	eng := delta.NewMatchEngine(blockSize, "md5")
	eng.LoadSignature(sig)
	instructions := eng.Search(newFile)

	// 3. Reconstruct
	recon := delta.NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(instructions)

	fmt.Println(bytes.Equal(result, newFile))
	fmt.Printf("Transfer: %.0f%%\n",
		float64(eng.LiteralBytes)/float64(len(newFile))*100)

	// Output:
	// true
	// Transfer: 73%
}
