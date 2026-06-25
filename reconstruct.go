package delta

import (
	"fmt"
	"hash"
	"io"
)

// Reconstructor rebuilds files on the receiver side.
// Reconstructor rebuilds files on the receiver side.
// Reconstructor 文件重建器（接收端）。
type Reconstructor struct {
	basisFile  []byte // local old file (basis) / 本地旧文件（基础文件）
	blockSize  int32
	blockLens  []int32 // per-block actual length; nil means all use blockSize / 每个块的实际长度，nil=全部用blockSize
	strongHash func() hash.Hash
}

// NewReconstructor creates a reconstructor.
// blockLens is optional: pass per-block actual lengths (from Signature.BlockSums[].Length)
// to avoid copying extra bytes from the last block. Pass nil to use blockSize uniformly,
// truncating by file length.
// NewReconstructor creates a reconstructor.
// blockLens is optional: pass per-block actual lengths (from Signature.BlockSums[].Length)
// to avoid copying extra bytes from the last block. Pass nil to use blockSize uniformly,
// truncating by file length.
// NewReconstructor 创建重建器。
// blockLens 可选：传入每个块的实际长度（通常来自 Signature.BlockSums[].Length），
// 避免最后一个块复制多余字节。传 nil 则全部使用 blockSize 并靠文件长度截断。
func NewReconstructor(basisFile []byte, blockSize int32, strongAlgo string, blockLens ...[]int32) *Reconstructor {
	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}
	rc := &Reconstructor{
		basisFile:  basisFile,
		blockSize:  blockSize,
		strongHash: algo.New,
	}
	if len(blockLens) > 0 && blockLens[0] != nil {
		rc.blockLens = blockLens[0]
	}
	return rc
}

// Reconstruct rebuilds a file from an instruction sequence.
// Reconstruct rebuilds a file from an instruction sequence.
// Reconstruct 根据指令序列重建文件。
func (rc *Reconstructor) Reconstruct(instructions []MatchResult) ([]byte, error) {
	// estimate size / 预估大小
	var result []byte

	for _, inst := range instructions {
		if inst.IsLiteral {

			result = append(result, inst.Data...)
		} else {
			// block reference: copy from basis file
			// 块引用：从基础文件复制
			start := int64(inst.BlockIdx) * int64(rc.blockSize)
			// prefer actual block length, otherwise use blockSize truncating by file length.
			// 优先使用实际块长，否则用 blockSize 并通过文件长度截断。
			blockLen := rc.blockSize
			if rc.blockLens != nil && inst.BlockIdx < len(rc.blockLens) {
				blockLen = rc.blockLens[inst.BlockIdx]
			}
			end := start + int64(blockLen)
			if end > int64(len(rc.basisFile)) {
				end = int64(len(rc.basisFile))
			}
			if start > int64(len(rc.basisFile)) {
				return nil, fmt.Errorf("block index %d out of basis file range / 块索引 %d 超出基础文件范围", inst.BlockIdx, inst.BlockIdx)
			}
			result = append(result, rc.basisFile[start:end]...)
		}
	}

	return result, nil
}

// WriteInstruction writes the output of a single instruction to w (streaming
// reconstruction, low memory). Literal instructions write Data directly;
// match instructions copy the corresponding block from the basis file.
// WriteInstruction writes the output of a single instruction to w (streaming
// reconstruction, low memory). Literal instructions write Data directly;
// match instructions copy the corresponding block from the basis file.
// WriteInstruction 将单条指令的输出写入 w（流式重建，低内存占用）。
// Literal 指令直接写 Data，Match 指令从 basisFile 复制对应块。
func (rc *Reconstructor) WriteInstruction(w io.Writer, inst MatchResult) error {
	if inst.IsLiteral {
		_, err := w.Write(inst.Data)
		return err
	}
	// block reference: copy from basis file
	// 块引用：从基础文件复制
	start := int64(inst.BlockIdx) * int64(rc.blockSize)
	blockLen := rc.blockSize
	if rc.blockLens != nil && inst.BlockIdx < len(rc.blockLens) {
		blockLen = rc.blockLens[inst.BlockIdx]
	}
	end := start + int64(blockLen)
	if end > int64(len(rc.basisFile)) {
		end = int64(len(rc.basisFile))
	}
	if start > int64(len(rc.basisFile)) {
		return fmt.Errorf("block index %d out of basis file range / 块索引 %d 超出基础文件范围", inst.BlockIdx, inst.BlockIdx)
	}
	_, err := w.Write(rc.basisFile[start:end])
	return err
}

// Verify checks the reconstructed result against an expected strong checksum.
// Verify checks the reconstructed result against an expected strong checksum.
// Verify 验证重建结果。
func (rc *Reconstructor) Verify(result []byte, expectedSum []byte) bool {
	h := rc.strongHash()
	h.Reset()
	h.Write(result)
	actual := h.Sum(nil)

	if len(actual) != len(expectedSum) {
		return false
	}
	for i := range actual {
		if actual[i] != expectedSum[i] {
			return false
		}
	}
	return true
}
