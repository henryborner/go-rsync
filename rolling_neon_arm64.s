// checksum1 ARM64 — 16B unrolled scalar. CHAR_OFFSET post-correction in Go.
// TODO: replace with NEON when Go 1.27 adds VUADDLP/VUMULL/VUMULL2/VSADDLP.
// GNU-as verified WORD encodings: see /memories/repo/go-rsync-info.md

#include "textflag.h"

// func checksum1NEON(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1NEON(SB), NOSPLIT, $0-41
	MOVD    data+0(FP), R0
	MOVD    data_len+8(FP), R1
	CMP     $32, R1
	BLT     bail

	MOVD    s1+24(FP), R2
	MOVD    s2+32(FP), R3
	MOVWU   (R2), R8
	MOVWU   (R3), R9

	ADD     R0, R1, R1

main_loop:
	SUB     R0, R1, R4
	CMP     $16, R4
	BLO     tail

	MOVBU   0(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   1(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   2(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   3(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   4(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   5(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   6(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   7(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   8(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   9(R0), R4;  ADD R4, R8;  ADD R8, R9
	MOVBU   10(R0), R4; ADD R4, R8;  ADD R8, R9
	MOVBU   11(R0), R4; ADD R4, R8;  ADD R8, R9
	MOVBU   12(R0), R4; ADD R4, R8;  ADD R8, R9
	MOVBU   13(R0), R4; ADD R4, R8;  ADD R8, R9
	MOVBU   14(R0), R4; ADD R4, R8;  ADD R8, R9
	MOVBU   15(R0), R4; ADD R4, R8;  ADD R8, R9

	ADD     $16, R0, R0
	B       main_loop

tail:
	CMP     R0, R1
	BHS     done
	MOVBU.P 1(R0), R4
	ADD     R4, R8
	ADD     R8, R9
	B       tail

done:
	MOVW    R8, (R2)
	MOVW    R9, (R3)
	MOVD    $1, R10
	MOVB    R10, ret+40(FP)
	RET

bail:
	MOVD    $0, R10
	MOVB    R10, ret+40(FP)
	RET
