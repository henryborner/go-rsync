//go:build amd64

#include "textflag.h"

// func cpuSHA() bool
TEXT ·cpuSHA(SB), NOSPLIT, $0-1
	MOVL $7, AX
	XORL CX, CX
	BYTE $0x0f; BYTE $0xa2   // CPUID
	TESTL $0x20000000, BX    // bit 29 = SHA
	JNE   yes
	MOVB $0, ret+0(FP)
	RET
yes:
	MOVB $1, ret+0(FP)
	RET
