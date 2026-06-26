// md5x8LoadTranspose — loads 64 bytes from 8 scattered block positions and
// transposes directly into 16 transposed YMM words. Eliminates the intermediate
// 512-byte copy (buf[8][64]byte) that the Go wrapper currently does.
//
// Args:
//   data    — the full source byte slice
//   offsets — 8 starting offsets within data for each block
//   chunk   — which 64-byte chunk to process (0, 1, 2, ...)
//   x       — output [16][8]uint32, 512 bytes (16 words × 8 lanes × 4 bytes)
//
// Registers: R12-R15,AX,BX,DX,DI = effective addresses for blocks 0..7
//            R11 = x output pointer, R8 = temp

#include "textflag.h"

// func md5x8LoadTransposeScalar(data []byte, offsets *[8]int, chunk int, x *[16][8]uint32)
TEXT ·md5x8LoadTransposeScalar(SB), NOSPLIT, $0-48
	MOVQ data+0(FP), R8       // data base pointer
	MOVQ offsets+24(FP), R9    // &offsets[0]
	MOVQ chunk+32(FP), R10     // chunk index
	MOVQ x+40(FP), R11         // x output pointer

	// CX = chunk * 64
	MOVQ R10, CX
	SHLQ $6, CX

	// Compute effective address for each of 8 blocks: eff[b] = data + offsets[b] + chunk*64
	MOVQ (R9), R12; ADDQ R8, R12; ADDQ CX, R12     // eff[0]
	MOVQ 8(R9), R13; ADDQ R8, R13; ADDQ CX, R13    // eff[1]
	MOVQ 16(R9), R14; ADDQ R8, R14; ADDQ CX, R14   // eff[2]
	MOVQ 24(R9), R15; ADDQ R8, R15; ADDQ CX, R15   // eff[3]
	MOVQ 32(R9), AX;  ADDQ R8, AX;  ADDQ CX, AX    // eff[4]
	MOVQ 40(R9), BX;  ADDQ R8, BX;  ADDQ CX, BX    // eff[5]
	MOVQ 48(R9), DX;  ADDQ R8, DX;  ADDQ CX, DX    // eff[6]
	MOVQ 56(R9), DI;  ADDQ R8, DI;  ADDQ CX, DI    // eff[7]

	// ── Word 0 (byte offset 0 within each 64-byte block) ──
	MOVL 0(R12), R8; VMOVD R8, X0
	MOVL 0(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 0(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 0(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 0(AX), R8; VMOVD R8, X1
	MOVL 0(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 0(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 0(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 0(R11)

	// ── Word 1 (byte offset 4) ──
	MOVL 4(R12), R8; VMOVD R8, X0
	MOVL 4(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 4(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 4(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 4(AX), R8; VMOVD R8, X1
	MOVL 4(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 4(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 4(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 32(R11)

	// ── Word 2 (byte offset 8) ──
	MOVL 8(R12), R8; VMOVD R8, X0
	MOVL 8(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 8(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 8(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 8(AX), R8; VMOVD R8, X1
	MOVL 8(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 8(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 8(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 64(R11)

	// ── Word 3 (byte offset 12) ──
	MOVL 12(R12), R8; VMOVD R8, X0
	MOVL 12(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 12(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 12(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 12(AX), R8; VMOVD R8, X1
	MOVL 12(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 12(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 12(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 96(R11)

	// ── Word 4 (byte offset 16) ──
	MOVL 16(R12), R8; VMOVD R8, X0
	MOVL 16(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 16(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 16(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 16(AX), R8; VMOVD R8, X1
	MOVL 16(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 16(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 16(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 128(R11)

	// ── Word 5 (byte offset 20) ──
	MOVL 20(R12), R8; VMOVD R8, X0
	MOVL 20(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 20(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 20(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 20(AX), R8; VMOVD R8, X1
	MOVL 20(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 20(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 20(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 160(R11)

	// ── Word 6 (byte offset 24) ──
	MOVL 24(R12), R8; VMOVD R8, X0
	MOVL 24(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 24(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 24(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 24(AX), R8; VMOVD R8, X1
	MOVL 24(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 24(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 24(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 192(R11)

	// ── Word 7 (byte offset 28) ──
	MOVL 28(R12), R8; VMOVD R8, X0
	MOVL 28(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 28(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 28(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 28(AX), R8; VMOVD R8, X1
	MOVL 28(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 28(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 28(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 224(R11)

	// ── Word 8 (byte offset 32) ──
	MOVL 32(R12), R8; VMOVD R8, X0
	MOVL 32(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 32(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 32(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 32(AX), R8; VMOVD R8, X1
	MOVL 32(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 32(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 32(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 256(R11)

	// ── Word 9 (byte offset 36) ──
	MOVL 36(R12), R8; VMOVD R8, X0
	MOVL 36(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 36(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 36(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 36(AX), R8; VMOVD R8, X1
	MOVL 36(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 36(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 36(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 288(R11)

	// ── Word 10 (byte offset 40) ──
	MOVL 40(R12), R8; VMOVD R8, X0
	MOVL 40(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 40(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 40(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 40(AX), R8; VMOVD R8, X1
	MOVL 40(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 40(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 40(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 320(R11)

	// ── Word 11 (byte offset 44) ──
	MOVL 44(R12), R8; VMOVD R8, X0
	MOVL 44(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 44(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 44(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 44(AX), R8; VMOVD R8, X1
	MOVL 44(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 44(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 44(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 352(R11)

	// ── Word 12 (byte offset 48) ──
	MOVL 48(R12), R8; VMOVD R8, X0
	MOVL 48(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 48(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 48(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 48(AX), R8; VMOVD R8, X1
	MOVL 48(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 48(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 48(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 384(R11)

	// ── Word 13 (byte offset 52) ──
	MOVL 52(R12), R8; VMOVD R8, X0
	MOVL 52(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 52(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 52(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 52(AX), R8; VMOVD R8, X1
	MOVL 52(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 52(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 52(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 416(R11)

	// ── Word 14 (byte offset 56) ──
	MOVL 56(R12), R8; VMOVD R8, X0
	MOVL 56(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 56(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 56(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 56(AX), R8; VMOVD R8, X1
	MOVL 56(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 56(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 56(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 448(R11)

	// ── Word 15 (byte offset 60) ──
	MOVL 60(R12), R8; VMOVD R8, X0
	MOVL 60(R13), R8; VPINSRD $1, R8, X0, X0
	MOVL 60(R14), R8; VPINSRD $2, R8, X0, X0
	MOVL 60(R15), R8; VPINSRD $3, R8, X0, X0
	MOVL 60(AX), R8; VMOVD R8, X1
	MOVL 60(BX), R8; VPINSRD $1, R8, X1, X1
	MOVL 60(DX), R8; VPINSRD $2, R8, X1, X1
	MOVL 60(DI), R8; VPINSRD $3, R8, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	VMOVDQU Y0, 480(R11)

	VZEROUPPER
	RET
