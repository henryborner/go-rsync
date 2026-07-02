package delta

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"io"
	"testing"
)

// ── Delta roundtrip fuzz ─────────────────────────────────────────────

func FuzzDeltaRoundTrip(t *testing.F) {
	// Seed corpus: small, medium, identical, fully-different.
	seeds := []struct {
		oldLen, newLen int
		modPct         int // 0 = identical, 100 = fully different
	}{
		{100, 100, 0},
		{100, 120, 10},
		{1000, 1000, 0},
		{1000, 1050, 5},
		{10000, 10000, 50},
		{10000, 9000, 100},
	}
	for _, s := range seeds {
		old := make([]byte, s.oldLen)
		rand.Read(old)
		newF := make([]byte, s.newLen)
		copy(newF, old[:min(s.oldLen, s.newLen)])
		if s.newLen > s.oldLen {
			rand.Read(newF[s.oldLen:])
		}
		if s.modPct > 0 && s.modPct < 100 {
			for i := 0; i < len(newF)/max(100/s.modPct, 1); i++ {
				newF[i*max(100/s.modPct, 1)] ^= 0xFF
			}
		}
		t.Add(old, newF, int32(32), "md5")
	}

	t.Fuzz(func(t *testing.T, oldFile, newFile []byte, blockSize int32, algo string) {
		if blockSize < 16 || blockSize > 1024*1024 {
			return
		}
		if algo != "md5" && algo != "sha256" && algo != "xxh64" {
			return
		}
		if len(oldFile) == 0 || len(newFile) == 0 {
			return
		}

		sig := GenerateSignature(oldFile, blockSize, algo)

		// Serial path
		eng := NewMatchEngine(blockSize, algo)
		eng.LoadSignature(sig)
		serial := eng.Search(newFile)

		recon := NewReconstructor(oldFile, blockSize, algo)
		result, err := recon.Reconstruct(serial)
		if err != nil {
			t.Fatalf("reconstruct serial: %v", err)
		}
		if !bytes.Equal(result, newFile) {
			t.Fatalf("serial roundtrip mismatch: old=%d new=%d result=%d blockSize=%d algo=%s",
				len(oldFile), len(newFile), len(result), blockSize, algo)
		}

		// Streaming path parity
		eng2 := NewMatchEngine(blockSize, algo)
		eng2.LoadSignature(sig)
		var streamResults []MatchResult
		err = eng2.SearchReader(bytes.NewReader(newFile), int64(len(newFile)), func(mr MatchResult) error {
			cp := mr
			if mr.IsLiteral {
				cp.Data = make([]byte, len(mr.Data))
				copy(cp.Data, mr.Data)
			}
			streamResults = append(streamResults, cp)
			return nil
		})
		if err != nil {
			t.Fatalf("SearchReader: %v", err)
		}

		recon2 := NewReconstructor(oldFile, blockSize, algo)
		result2, err := recon2.Reconstruct(streamResults)
		if err != nil {
			t.Fatalf("reconstruct stream: %v", err)
		}
		if !bytes.Equal(result2, newFile) {
			t.Fatalf("stream roundtrip mismatch: old=%d new=%d result=%d", len(oldFile), len(newFile), len(result2))
		}
	})
}

// ── Wire protocol fuzz ──────────────────────────────────────────────

func FuzzWireSignature(t *testing.F) {
	seeds := []int{100, 500, 1000, 10000}
	for _, sz := range seeds {
		data := make([]byte, sz)
		rand.Read(data)
		t.Add(data, int32(64), "md5")
	}

	t.Fuzz(func(t *testing.T, data []byte, blockSize int32, algo string) {
		if blockSize < 16 || blockSize > 128*1024 {
			return
		}
		if algo != "md5" && algo != "sha256" && algo != "xxh64" && algo != "xxh3" {
			return
		}
		if len(data) < int(blockSize) {
			return
		}

		sig := GenerateSignature(data, blockSize, algo)

		var buf bytes.Buffer
		if err := WireEncodeSignature(&buf, sig); err != nil {
			t.Fatalf("encode: %v", err)
		}

		decoded, err := WireDecodeSignature(&buf)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		if decoded.BlockSize != sig.BlockSize || decoded.FileSize != sig.FileSize {
			t.Fatalf("header mismatch")
		}
		if len(decoded.BlockSums) != len(sig.BlockSums) {
			t.Fatalf("block count mismatch: %d vs %d", len(decoded.BlockSums), len(sig.BlockSums))
		}
		for i := range sig.BlockSums {
			a, b := sig.BlockSums[i], decoded.BlockSums[i]
			if a.Index != b.Index || a.Sum1 != b.Sum1 || a.Offset != b.Offset || a.Length != b.Length {
				t.Fatalf("block[%d] mismatch", i)
			}
			if !bytes.Equal(a.Sum2, b.Sum2) {
				t.Fatalf("block[%d] sum2 mismatch", i)
			}
		}
	})
}

func FuzzWireInstructions(t *testing.F) {
	// Build realistic instruction sequences.
	seeds := []int{100, 1000, 10000}
	for _, sz := range seeds {
		data := make([]byte, sz)
		rand.Read(data)
		bs := CalculateBlockSize(int64(sz))
		sig := GenerateSignature(data, bs, "md5")
		eng := NewMatchEngine(bs, "md5")
		eng.LoadSignature(sig)
		insts := eng.Search(data)
		t.Add(data, bs, "md5")
		_ = insts
	}

	t.Fuzz(func(t *testing.T, data []byte, blockSize int32, algo string) {
		if blockSize < 16 || blockSize > 128*1024 {
			return
		}
		if algo != "md5" {
			return
		}
		if len(data) < int(blockSize) {
			return
		}

		sig := GenerateSignature(data, blockSize, algo)
		eng := NewMatchEngine(blockSize, algo)
		eng.LoadSignature(sig)
		insts := eng.Search(data)

		var buf bytes.Buffer
		if err := WireEncodeInstructions(&buf, insts); err != nil {
			t.Fatalf("encode: %v", err)
		}

		var decoded []MatchResult
		err := DecodeInstructionsStream(&buf, func(mr MatchResult) error {
			cp := mr
			if mr.IsLiteral {
				cp.Data = make([]byte, len(mr.Data))
				copy(cp.Data, mr.Data)
			}
			decoded = append(decoded, cp)
			return nil
		})
		if err != nil && err != io.EOF {
			t.Fatalf("decode: %v", err)
		}

		if len(decoded) != len(insts) {
			t.Fatalf("instruction count mismatch: %d vs %d", len(decoded), len(insts))
		}
		for i := range insts {
			a, b := insts[i], decoded[i]
			if a.IsLiteral != b.IsLiteral {
				t.Fatalf("inst[%d] type mismatch", i)
			}
			if !a.IsLiteral && a.BlockIdx != b.BlockIdx {
				t.Fatalf("inst[%d] blockIdx: %d vs %d", i, a.BlockIdx, b.BlockIdx)
			}
			if a.IsLiteral && !bytes.Equal(a.Data, b.Data) {
				t.Fatalf("inst[%d] literal data mismatch", i)
			}
		}
	})
}

// ── Checksum parity fuzz ────────────────────────────────────────────

func FuzzChecksum1Parity(t *testing.F) {
	seeds := []int{0, 1, 31, 32, 33, 63, 64, 65, 127, 128, 255, 256, 700, 1024, 65536}
	for _, sz := range seeds {
		data := make([]byte, sz)
		if sz > 0 {
			rand.Read(data)
		}
		t.Add(data)
	}

	t.Fuzz(func(t *testing.T, data []byte) {
		// Pure Go reference (always available).
		want := Checksum1(data)

		// Compare with private checksum1 (uses AVX2/SSE2 on amd64).
		s1, s2 := checksum1(data)
		got := (s1 & 0xFFFF) | ((s2 & 0xFFFF) << 16)

		if got != want {
			// They may legitimately differ when len(data)==0, since Checksum1
			// returns 0 and checksum1 returns (0,0) → 0.  Check that case.
			if len(data) > 0 {
				t.Fatalf("len=%d: Checksum1=%08x checksum1→%08x (s1=%d s2=%d)",
					len(data), want, got, s1, s2)
			}
		}
	})
}

// ── MD5 SIMD parity fuzz ───────────────────────────────────────────

func FuzzMD5x8Parity(t *testing.F) {
	if !md5x8available() {
		t.Skip("AVX2 not available")
	}

	seeds := []int{64, 128, 700, 1024, 2048}
	for _, sz := range seeds {
		data := make([]byte, 8*sz)
		rand.Read(data)
		t.Add(data, sz)
	}

	t.Fuzz(func(t *testing.T, data []byte, blockLen int) {
		if blockLen < 1 || blockLen > 8192 {
			return
		}
		need := 8 * blockLen
		if len(data) < need {
			return
		}

		var offsets, lengths [8]int
		for b := 0; b < 8; b++ {
			offsets[b] = b * blockLen
			lengths[b] = blockLen
		}

		var outAVX2 [8][16]byte
		md5Hash8wayAVX2(data, offsets, lengths, &outAVX2)

		for b := 0; b < 8; b++ {
			expected := md5.Sum(data[offsets[b] : offsets[b]+lengths[b]])
			if outAVX2[b] != expected {
				t.Fatalf("lane %d (len=%d) mismatch:\n  AVX2: %x\n  md5:  %x",
					b, blockLen, outAVX2[b], expected)
			}
		}

		// Also compare against pure Go reference.
		var outGo [8][16]byte
		md5Hash8wayGo(data, offsets, lengths, &outGo)
		for b := 0; b < 8; b++ {
			if outAVX2[b] != outGo[b] {
				t.Fatalf("lane %d (len=%d) AVX2 vs Go:\n  AVX2: %x\n  Go:   %x",
					b, blockLen, outAVX2[b], outGo[b])
			}
		}
	})
}

// ── Reconstruct bad BlockIdx fuzz (regression for negative-index panic) ─

func FuzzReconstructBadBlockIdx(t *testing.F) {
	// Seed: valid zero index (should succeed).
	t.Add([]byte{0, 0, 0, 0}, int32(64))

	t.Fuzz(func(t *testing.T, blockIdxBytes []byte, blockSize int32) {
		if blockSize < 1 || blockSize > 128*1024 {
			return
		}
		if len(blockIdxBytes) < 4 {
			return
		}
		// Interpret raw bytes as int32 — can produce negative values.
		blockIdx := int(int32(blockIdxBytes[0])<<24 |
			int32(blockIdxBytes[1])<<16 |
			int32(blockIdxBytes[2])<<8 |
			int32(blockIdxBytes[3]))

		basis := make([]byte, int(blockSize)*10)

		recon := NewReconstructor(basis, blockSize, "md5")
		_, err := recon.Reconstruct([]MatchResult{
			{IsLiteral: false, BlockIdx: blockIdx},
		})
		if err == nil && blockIdx < 0 {
			t.Fatalf("expected error for negative BlockIdx=%d, got nil", blockIdx)
		}
		// Must not panic for any input.
	})
}

// ── WireDecodeSignature corrupt-input fuzz ──────────────────────────

func FuzzWireDecodeCorrupt(t *testing.F) {
	// Seed: valid empty signature (16-byte header, zero count).
	seed := make([]byte, 16)
	seed[3] = 1 // blockSize = 1 (valid, >0)
	t.Add(seed)
	// Seed: truncated headers.
	t.Add(make([]byte, 8))
	t.Add(make([]byte, 0))

	t.Fuzz(func(t *testing.T, data []byte) {
		// WireDecodeSignature must not panic on any input.
		sig, err := WireDecodeSignature(bytes.NewReader(data))
		if err == nil && sig != nil {
			// If decode succeeded, fields must be self-consistent.
			if sig.BlockSize <= 0 {
				t.Errorf("decoded invalid blockSize=%d without error", sig.BlockSize)
			}
			if sig.FileSize < 0 {
				t.Errorf("decoded negative fileSize=%d without error", sig.FileSize)
			}
		}
	})
}

// ── SearchReader error propagation fuzz ─────────────────────────────

type errorReader struct {
	data   []byte
	errAt  int // return error after this many bytes read
	errVal error
}

func (r *errorReader) Read(p []byte) (int, error) {
	if r.errAt <= 0 {
		return 0, r.errVal
	}
	n := len(p)
	if n > r.errAt {
		n = r.errAt
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	r.errAt -= n
	if r.errAt <= 0 {
		return n, r.errVal
	}
	return n, nil
}

func FuzzSearchReaderError(t *testing.F) {
	// errType: 0=io.EOF, 1=io.ErrUnexpectedEOF, 2=io.ErrClosedPipe (real error)
	// Seeds cover: early EOF, late EOF, real read failure.
	seedBasis := make([]byte, 2000)
	t.Add(seedBasis, int32(64), "md5", int16(100), int16(0))  // EOF early
	t.Add(seedBasis, int32(64), "md5", int16(1000), int16(1)) // ErrUnexpectedEOF
	t.Add(seedBasis, int32(64), "md5", int16(500), int16(2))  // real I/O error

	t.Fuzz(func(t *testing.T, basis []byte, blockSize int32, algo string, errAfter int16, errType int16) {
		if blockSize < 16 || blockSize > 1024 {
			return
		}
		if algo != "md5" {
			return
		}
		if len(basis) < int(blockSize)*3 {
			return
		}
		if errAfter < 0 {
			return
		}

		sig := GenerateSignature(basis, blockSize, algo)
		eng := NewMatchEngine(blockSize, algo)
		eng.LoadSignature(sig)

		var errVal error
		switch errType % 3 {
		case 0:
			errVal = io.EOF
		case 1:
			errVal = io.ErrUnexpectedEOF
		default:
			errVal = io.ErrClosedPipe // real I/O error (non-EOF)
		}

		fileSize := int64(len(basis))
		r := &errorReader{
			data:   make([]byte, fileSize),
			errAt:  int(errAfter),
			errVal: errVal,
		}
		copy(r.data, basis)

		var results []MatchResult
		err := eng.SearchReader(r, fileSize, func(mr MatchResult) error {
			cp := mr
			if mr.IsLiteral {
				cp.Data = make([]byte, len(mr.Data))
				copy(cp.Data, mr.Data)
			}
			results = append(results, cp)
			return nil
		})

		// For real I/O errors (non-EOF), the error must propagate.
		if errVal != io.EOF && errVal != io.ErrUnexpectedEOF {
			if err == nil {
				t.Fatalf("expected error for errType=%d errVal=%v, got nil", errType, errVal)
			}
			return
		}

		// For EOF variants: no fatal error, and results must not panic on reconstruct.
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(results) > 0 {
			recon := NewReconstructor(basis, blockSize, algo)
			if _, recErr := recon.Reconstruct(results); recErr != nil {
				// Partial reconstruction may fail — just don't panic.
			}
		}
	})
}
