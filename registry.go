// Package delta implements the rsync delta-transfer algorithm in Go.
//
// It provides a complete toolkit for binary delta compression:
// rolling checksum matching (rsync-style), block signature generation,
// instruction-based file reconstruction, and a binary wire protocol
// for sending deltas over the network.
//
// # Key features
//
//   - Tiered checksum engine: AVX2 (64B/iter, ~77 GB/s) → SSE2 (32B/iter,
//     ~39 GB/s) → pure Go 128B batch on amd64; portable byte-by-byte on
//     other architectures.  Automatic CPU dispatch at runtime.
//   - Pluggable strong hash: md5, sha256, xxh64, xxh3-128 built-in.  Register
//     your own via [Register].
//   - Streaming I/O: generate signatures from [io.Reader], decode
//     instructions one-at-a-time with [DecodeInstructionsStream], and
//     reconstruct files with minimal memory.
//   - Binary wire format: compact big-endian encoding for signatures and
//     instructions — ready to pipe over SSH or any [io.Writer].
//
// # Quick start
//
//	// --- Sender side ---
//	sig := delta.GenerateSignature(oldFile, blockSize, "md5")
//	eng := delta.NewMatchEngine(blockSize, "md5")
//	eng.LoadSignature(sig)
//	insts := eng.Search(newFile)
//	delta.WireEncodeInstructions(os.Stdout, insts)
//
//	// --- Receiver side ---
//	sig, _ := delta.WireDecodeSignature(os.Stdin)
//	recon := delta.NewReconstructor(oldFile, blockSize, "md5")
//	delta.DecodeInstructionsStream(os.Stdin, func(inst delta.MatchResult) error {
//	    return recon.WriteInstruction(os.Stdout, inst)
//	})
//
// # Relationship to rsync
//
// The rolling checksum uses CHAR_OFFSET=31.
// (Note: standard rsync builds use CHAR_OFFSET=0; not wire-compatible.)
// The hash-table matching and block-size goal (~√N blocks) follow the same
// approach.  This package is NOT a wire-compatible
// rsync client — it provides the building blocks so you can embed delta
// compression in your own tools.
//
// # Performance (amd64, Ryzen 9 8940HX)
//
//	checksum1 throughput:
//	  AVX2      ~77 GB/s
//	  SSE2      ~39 GB/s
//	  pure Go    ~1.9 GB/s
//
// See the project README for full benchmark tables.
//
// Package delta allows registering custom hash algorithms, replacing hard-coded switches.
// 允许用户注册自定义哈希算法，替换硬编码的 switch。
package delta

import (
	"fmt"
	"hash"
	"sync"
)

// ChecksumAlgo describes a checksum algorithm.
// ChecksumAlgo 描述一个校验和算法。
type ChecksumAlgo struct {
	Name   string           // algorithm name e.g. "md5", "sha256", "xxh3" / 算法名称
	New    func() hash.Hash // hash constructor / 哈希构造函数
	Length int              // output bytes / 输出字节数

	// FastSum is an optional zero-allocation fast path for in-memory hashing.
	// It computes the hash of data and writes the result to out[:Length].
	// out is a pre-allocated buffer with at least Length bytes of capacity.
	// Returns out[:Length] (may be a subslice of out).
	// If nil, the hash.Hash path (New+Write+Sum) is used instead.
	// FastSum 是可选的零分配快速哈希路径。直接计算 data 的哈希写入 out[:Length]，
	// out 是预分配的缓冲区（容量 ≥ Length）。返回 out[:Length]。若为 nil，走 hash.Hash 路径。
	FastSum func(out, data []byte) []byte
}

var (
	registryMu  sync.RWMutex
	registry    = make(map[string]ChecksumAlgo)
	defaultAlgo = "md5"
)

func init() {

	Register(ChecksumAlgo{
		Name:    "md5",
		New:     newMD5,
		Length:  16,
		FastSum: md5FastSum,
	})
	Register(ChecksumAlgo{
		Name:    "sha256",
		New:     newSHA256,
		Length:  32,
		FastSum: sha256FastSum,
	})
	Register(ChecksumAlgo{
		Name:    "xxh64",
		New:     newXXH64,
		Length:  8,
		FastSum: xxh64FastSum,
	})
	Register(ChecksumAlgo{
		Name:    "xxh3",
		New:     newXXH3,
		Length:  16,
		FastSum: xxh3FastSum,
	})
}

// Register registers a checksum algorithm (thread-safe).
// Register 注册一个校验和算法（线程安全）。
func Register(algo ChecksumAlgo) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[algo.Name] = algo
}

// GetAlgo retrieves a registered algorithm.
// GetAlgo 获取已注册的算法。
func GetAlgo(name string) (ChecksumAlgo, error) {
	registryMu.RLock()
	algo, ok := registry[name]
	registryMu.RUnlock() // release before ListAlgos() to avoid deadlock / 在 ListAlgos() 前释放，避免死锁
	if !ok {
		return ChecksumAlgo{}, fmt.Errorf("unknown checksum algorithm / 未知校验和算法: %s (registered / 已注册: %v)", name, ListAlgos())
	}
	return algo, nil
}

func ListAlgos() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// SetDefault sets the default algorithm.
// SetDefault 设置默认算法。
func SetDefault(name string) error {
	if _, err := GetAlgo(name); err != nil {
		return err
	}
	registryMu.Lock()
	defaultAlgo = name
	registryMu.Unlock()
	return nil
}

// GetDefault returns the current default algorithm name.
// GetDefault 获取默认算法名称。
func GetDefault() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return defaultAlgo
}

func MustGet(name string) ChecksumAlgo {
	algo, err := GetAlgo(name)
	if err != nil {
		panic(err)
	}
	return algo
}

func NewHash(algoName string) (hash.Hash, error) {
	algo, err := GetAlgo(algoName)
	if err != nil {
		return nil, err
	}
	return algo.New(), nil
}
