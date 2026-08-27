module github.com/henryborner/go-rsync

go 1.26

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/henryborner/go-rsync/hashsimd v0.0.0-00010101000000-000000000000
	github.com/zeebo/xxh3 v1.1.0
	golang.org/x/sys v0.47.0
)

require github.com/klauspost/cpuid/v2 v2.2.10 // indirect

replace github.com/henryborner/go-rsync/hashsimd => ./hashsimd
