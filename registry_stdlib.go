package delta

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"hash"

	xxhash "github.com/cespare/xxhash/v2"
)

func newMD5() hash.Hash    { return md5.New() }
func newSHA256() hash.Hash { return sha256.New() }
func newXXH64() hash.Hash  { return xxhash.New() }

// FastSum implementations bypass the hash.Hash interface entirely.
// For 700-byte blocks this eliminates ~900ns of Reset+Write+Sum overhead.

func md5FastSum(out, data []byte) []byte {
	sum := md5.Sum(data) // [16]byte on stack, zero alloc
	copy(out, sum[:])
	return out[:16]
}

func sha256FastSum(out, data []byte) []byte {
	sum := sha256.Sum256(data) // [32]byte on stack, zero alloc
	copy(out, sum[:])
	return out[:32]
}

func xxh64FastSum(out, data []byte) []byte {
	binary.BigEndian.PutUint64(out, xxhash.Sum64(data))
	return out[:8]
}
