package delta

import (
	"bytes"
	"io"
)

// ── High-level convenience API ──────────────────────────────────────────
// These functions wrap the core pipeline (GenerateSignature → Search →
// Reconstruct) into single-call operations for common use cases.
// 高层便捷 API，将核心管线封装为单次调用，方便常见场景使用。

// Delta computes the instruction stream (delta) between oldFile and newFile.
// It combines GenerateSignature + NewMatchEngine + LoadSignature + Search
// into a single call.
//
// Delta 计算 oldFile 与 newFile 之间的指令流（delta）。
// 等同于 GenerateSignature + NewMatchEngine + LoadSignature + Search 的组合。
func Delta(oldFile, newFile []byte, blockSize int32, algo string) []MatchResult {
	sig := GenerateSignature(oldFile, blockSize, algo)
	eng := NewMatchEngine(blockSize, algo)
	eng.LoadSignature(sig)
	return eng.Search(newFile)
}

// DeltaFromWire reads a wire-encoded signature from r, then computes the
// delta against newFile.  Returns both the instruction stream and the
// MatchEngine so callers can inspect LiteralBytes for perfect-match
// optimisations.
//
// DeltaFromWire 从 r 读取 wire 格式的签名，然后计算与 newFile 的 delta。
// 同时返回指令流和 MatchEngine，以便调用者检查 LiteralBytes 实现完全匹配优化。
func DeltaFromWire(r io.Reader, newFile []byte, algo string) ([]MatchResult, *MatchEngine, error) {
	sig, err := WireDecodeSignature(r)
	if err != nil {
		return nil, nil, err
	}
	eng := NewMatchEngine(sig.BlockSize, algo)
	eng.LoadSignature(sig)
	return eng.Search(newFile), eng, nil
}

// ApplyDelta reconstructs newFile from basisFile and an instruction stream.
// Wraps NewReconstructor + Reconstruct.
//
// ApplyDelta 根据基础文件和指令流量建新文件。
// 等同于 NewReconstructor + Reconstruct。
func ApplyDelta(basisFile []byte, insts []MatchResult, blockSize int32, algo string) ([]byte, error) {
	recon := NewReconstructor(basisFile, blockSize, algo)
	return recon.Reconstruct(insts)
}

// RoundTrip computes the delta between oldFile and newFile, then applies it
// to reconstruct the result.  Returns an error if the reconstruction does
// not match newFile byte-for-byte.  Useful for testing and validation.
//
// RoundTrip 计算 delta 并重建结果，验证与 newFile 逐字节一致。
// 主要用于测试和验证。
func RoundTrip(oldFile, newFile []byte, blockSize int32, algo string) ([]byte, error) {
	insts := Delta(oldFile, newFile, blockSize, algo)
	result, err := ApplyDelta(oldFile, insts, blockSize, algo)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(result, newFile) {
		return result, io.ErrUnexpectedEOF
	}
	return result, nil
}

// ApplyDeltaStream reads wire-encoded instructions from r, applies them to
// basisFile, and writes the reconstructed file to w.  Uses streaming I/O so
// neither the instruction list nor the output is fully buffered in memory.
// Suitable for piping over SSH.
//
// ApplyDeltaStream 从 r 读取 wire 格式的指令流，应用到 basisFile 并将结果
// 写入 w。使用流式 I/O，指令和输出都不会完全缓存在内存中。适用于 SSH 管道场景。
func ApplyDeltaStream(basisFile []byte, r io.Reader, w io.Writer, blockSize int32, algo string) error {
	recon := NewReconstructor(basisFile, blockSize, algo)
	return DecodeInstructionsStream(r, func(inst MatchResult) error {
		return recon.WriteInstruction(w, inst)
	})
}
