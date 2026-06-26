# go-rsync MD5 SIMD notes

## Architecture

```
GenerateSignature(data, blockSize) → GenerateSignatureReader(io.Reader, ...)
│
├─ Checksum1 (weak) ──────────────────────────────────────────
│   ├─ [amd64] AVX2 64B/iter  → rolling_amd64.s
│   ├─ [amd64] SSE2 32B/iter  → rolling_sse2_amd64.s
│   └─ [!amd64] byte loop     → rolling_generic.go
│
└─ strong hash (MD5) ─────────────────────────────────────────
    ├─ Phase 1: N full 64B chunks, batched SIMD
    │     load+gather → md5x8core (or md5x16core)
    ├─ Phase 2: tail + padding (md5Finalize8way / scalar)
    └─ [!amd64] crypto/md5 stub

Path selection:  AVX512 (blockSize ≥ 2KB) → AVX2 → scalar
```

## Key files

| File | Role |
|------|------|
| `md5x8_amd64.s` | 8-way AVX2 core, **generated** by `gen_md5x8/main.go` |
| `md5x8_gather_amd64.s` | **Raw machine code** VPGATHERDD load+transpose (BYTE opcodes) |
| `md5x8_load_transpose_amd64.s` | VPINSRD scalar fallback (~288 insn/chunk, always correct) |
| `md5x8_purego.go` | Pure-Go 8-way MD5 reference (used for validation & non-AVX2 fallback) |
| `md5x8_transpose.s` | Contiguous 8×64→16 YMM transpose (tail only) |
| `md5x8_amd64.go` | Glue: `md5Hash8wayAVX2`, finalization |
| `md5x8_common.go` | `t256[]`, `shifts[]`, `md5FinalLane` |
| `gen_md5x8/main.go` | Codegen for AVX2 core |
| `md5x16_amd64.s` | 16-way AVX512 core, **generated** by `gen_md5x16/main.go` |
| `md5x16_gather_amd64.s` | ZMM VPGATHERDD (k-mask + GPR index) |
| `md5x16_amd64.go` | Glue: `md5Hash16wayAVX512` |
| `gen_md5x16/main.go` | Codegen for AVX512 core |
| `rolling_amd64.s` | AVX2 rolling checksum (64B/iter) |
| `rolling_sse2_amd64.s` | SSE2 fallback (32B/iter) |
| `rolling_fast_amd64.go` | `Checksum1` fast path + 4-byte remainder |

## Techniques

**Data layout.**  Blocks are packed contiguously in a batch buffer (8× or 16×
blockSize).  Each 64B chunk is transposed to SoA: word `w` becomes a YMM/ZMM of
8/16 lanes.  The core reads X[g] as a single aligned `VMOVDQU`.

**Loading.**  `VPGATHERDD` gathers 8 (or 16) dwords from scattered positions in
one instruction.  AVX2 uses YMM mask (`VPCMPEQD Y,Y,Y` for all-ones); AVX512
uses k-mask (`KXNORW K1,K1,K1`).

**⚠️ VPGATHERDD mask zeroing.**  VPGATHERDD **zeros the mask register** after
execution.  Must reload the mask before **every** gather: pre-compute all-ones
in a spare register (e.g. `VPCMPEQD Y3,Y3,Y3`) then `VMOVDQA Y3,Y2` before
each gather.  Without reload, subsequent gathers silently read zero data.

**⚠️ Go assembler VSIB bug.**  Go Plan 9's VPGATHERDD encoding has multiple
bugs: base register is hardcoded (changing R8/R12/BP values at runtime has no
effect), VSIB displacement field produces wrong addresses, and non-Y1
destination registers may return zeros.  The workaround: emit raw machine code
via `BYTE` pseudo-instructions, bypassing the assembler entirely.

Encoding for `VPGATHERDD Y2, disp(R8)(Y7*2), Y1`:
```
C4 C2 6D 90 [ModRM] 78 [disp8]
    ↑ VEX3   ↑ opcode  ↑ VSIB (scale=×2,idx=Y7,base=R8)
```
Word 0 (no disp): `BYTE $0xC4; BYTE $0xC2; BYTE $0x6D; BYTE $0x90; BYTE $0x0C; BYTE $0x78`
Word N (disp=N*4): same but ModRM=`4C` + trailing `BYTE $disp`

**Implicit register rotation.**  Y0-Y3 hold (a,b,c,d) but logical roles permute
per step via a compile-time table, avoiding 5 `VMOVDQA` per step.  Only one
write: `VPADDD rb, temp, ra`.  Same pattern for ZMM in AVX512.

**Merged T-constant loads.**  `VPADDD T_i<>(SB), Y12, Y12` (memory-source add)
replaces `VMOVDQU + VPADDD`.  Constants stored as 8×/16× inflated DATA tables.

**Software pipelining (X prefetch).**  Load `X[g_{i+1}]` at the *end* of step
`i` (overlapping with writeback), rather than loading `X[g_i]` at the start on
the critical path.  Large win on CPUs with shallow OoO windows (+67% on Zen 4);
negligible on deep-window Xeons.

**Save-add.**  MD5 requires `digest += initial` after each block.  Initial
state saved to Y4-Y7 (Z4-Z7) on entry, added back before storing.

**AVX512 µop advantage.**  `VPTERNLOGD` does F in 1 insn (vs 3 for AVX2);
`VPROLD` rotates in 1 (vs 3).  With 2× lanes, ~3× theoretical throughput.

**Threshold.**  AVX512 gather overhead hurts small blocks.  `blockSize ≥ 2048`
activates AVX512; smaller uses AVX2.

## Regenerating

```bash
cd gen_md5x8 && go run .    # → ../md5x8_amd64.s
cd gen_md5x16 && go run .   # → ../md5x16_amd64.s
```

## Safety checklist

- [ ] `go vet .` — zero warnings
- [ ] `go test -count=1 .` — all tests pass
- [ ] `VZEROUPPER` before every `RET` in YMM/ZMM functions
- [ ] `//go:noescape` on all asm decls with pointer args
- [ ] Slice args occupy 24 bytes in frame (ptr+len+cap), not 16
- [ ] Non-amd64 build: `GOOS=darwin GOARCH=arm64 go build .` succeeds

## Quirks

- **VPANDN operand order.**  Go Plan 9 `VPANDN A,B,C` = `C = A &^ B`
  (not Intel's `C = ~A & B`).  The generator (`gen_md5x8/main.go`) was
  fixed to swap operands for Round 1/2/4.
- **VPGATHERDD syntax.**  Go asm order is `(base)(idx*scale), mask, dst` —
  not Intel's `mask, mem, dst`.  AVX512 needs `K1` (k-mask), not `YMM`.
- **VPGATHERDD mask init.**  Must explicitly set to all-ones
  (`VPCMPEQD Y,Y,Y` for YMM; `KXNORW K1,K1,K1` for k-mask).  Uninitialised
  masks silently zero-fill.
- **VPGATHERDD mask zeroing.**  Instruction zeros mask after execution.
  Must reload before each gather — pre-compute all-ones in spare register.
- **VPGATHERDD raw machine code.**  Go assembler VSIB encoding has bugs
  (hardcoded base reg, broken displacement).  Workaround: `BYTE` opcodes
  (see Loading section above for encoding).
- **`VPMADDUBSW` operand order.**  Go asm: signed first, unsigned second.
  Intel manual has them swapped.
- **PowerShell encoding.**  `Set-Content` → UTF-16.  Edit `.s` files with
  an editor, not shell redirection.
- **Cross-compilation.**  `$env:GOOS` persists in PowerShell.  Clear with
  `$env:GOOS=""` after cross-compiling.
