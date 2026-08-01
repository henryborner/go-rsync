package delta

const (
	CHAR_OFFSET = 31
)

type RollingSum struct {
	count int32 // bytes in window / 窗口中的字节数
	s1    uint32
	s2    uint32
}

func NewRollingSum(data []byte) *RollingSum {
	rs := &RollingSum{}
	rs.Reset(data)
	return rs
}

func (rs *RollingSum) Reset(data []byte) {
	rs.count = int32(len(data))
	rs.s1, rs.s2 = checksum1(data)
	// Only the low 16 bits of s1/s2 ever escape (Value/S1/S2).  Truncate here
	// so Roll can stay 16-bit — equivalent to rsync's per-roll & 0xFFFF, and
	// makes Value() a single OR.
	// 只有 s1/s2 的低 16 位会对外（Value/S1/S2）。这里直接截断，让 Roll 全程
	// 16 位运算——等价于 rsync 每轮 & 0xFFFF，并让 Value() 变为一条 OR。
	rs.s1 &= 0xFFFF
	rs.s2 &= 0xFFFF
}

// Roll advances the rolling window: removes one old byte, adds one new byte,
// and updates s1/s2. All arithmetic is uint32 with natural overflow —
// no modulo, no widening, no floating point.
// Roll 滚动窗口：移除一个旧字节，加入一个新字节，更新 s1/s2。
// 全部使用 uint32 算术并自然溢出——无取模、无拓宽、无浮点。
func (rs *RollingSum) Roll(oldByte, newByte byte, blockLen int32) {
	old := uint32(oldByte) + CHAR_OFFSET
	new := uint32(newByte) + CHAR_OFFSET

	// Pure uint32 arithmetic, overflow = natural modulo.
	// 同 rsync checksum.c：纯 uint32 运算，溢出自然取模。
	// s1/s2 stay truncated to 16 bits: (x mod 2^32) & 0xFFFF == x mod 2^16,
	// so keeping 16-bit state is bit-identical to the 32-bit version.
	rs.s1 = (rs.s1 + new - old) & 0xFFFF
	rs.s2 = (rs.s2 + rs.s1 - uint32(blockLen)*old) & 0xFFFF
}

func (rs *RollingSum) Value() uint32 {
	// s1/s2 are kept 16-bit, so packing is a single OR (no masks needed).
	// 组成 32-bit 校验和：s1/s2 已保持 16 位，只需一条 OR。
	return rs.s1 | (rs.s2 << 16)
}

// S1 returns the lower 16 bits of s1.
// S1 返回 s1 低 16 位。
func (rs *RollingSum) S1() uint32 { return rs.s1 & 0xFFFF }

// S2 returns the lower 16 bits of s2.
// S2 返回 s2 低 16 位。
func (rs *RollingSum) S2() uint32 { return rs.s2 & 0xFFFF }
