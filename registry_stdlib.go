package delta

import (
	"crypto/md5"
	"crypto/sha256"
	"hash"

	xxhash "github.com/cespare/xxhash/v2"
)

func newMD5() hash.Hash    { return md5.New() }
func newSHA256() hash.Hash { return sha256.New() }
func newXXH64() hash.Hash  { return xxhash.New() }
