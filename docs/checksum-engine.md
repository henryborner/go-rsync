# go-rsync Checksum Engine

> Originally developed for [Shuttle](https://github.com/henryborner/shuttle), now a standalone library.
>
> The checksum algorithm and deferred-reduction structure draw from studying rsync's `checksum.c` and `simd-checksum-avx2.S`. The VPMADDWD pair-sum approach and Go Plan 9 assembly adaptations are original work.

## 目录

- [1. 概述](#1-概述)
- [2. 算法](#2-算法)
- [3. 循环结构](#3-循环结构)
- [4. Go Plan 9 汇编注意事项](#4-go-plan-9-汇编注意事项)
- [5. 演进历史](#5-演进历史)
- [6. 已知 Bug 及修复](#6-已知-bug-及修复)
- [7. 寄存器映射](#7-寄存器映射)
- [8. 测试覆盖](#8-测试覆盖)
- [9. 性能数据](#9-性能数据)
- [10. 附录：SSE2 路径](#10-附录sse2-路径)
- [11. 附录：逐尺寸性能数据](#11-附录逐尺寸性能数据)

## 1. 概述

| Feature | go-rsync |
|---------|-----------|
| Data type | `uint8` (0..255) |
| CHAR_OFFSET | 31（比 rsync 默认的 0 更强，但不兼容标准 rsync。`Checksum1` 在 asm 内处理；私有 `checksum1` 在 Go 层后修正） |
| Return format | `Checksum1` 返回打包 `uint32`；`checksum1` 返回两个 `uint32` 标量 |
| s1 reduction | VPMADDWD pair-sum（全 32 位） |
| s2 weighted reduction | VPMADDWD pair-sum per half（全 32 位），无 VPUNPCK |
| PREFETCHT0 | 是（384 字节提前预取） |
| Loop instructions | **19**（v4 的 20 条基础上再减 1 条） |

> **核心思路**：s1 和 s2 都使用 VPMADDWD 做 pair-sum——一条指令将相邻 int16 对乘以 1 后求和。s2 值每半个 YMM 内不超过 32767（最大：64×255+63×255=32,385），无需 VPADDW 合并；两半分别 VPMADDWD 后用 int32 合并。两条路径都使用延迟归约。

## 2. 算法

### 2.1 每块分解（每次迭代 64 字节）

块 k（0 起始）：

```
s1_before_k       = 块 k 开始时的累积 s1
delta_s1_k        = 块 k 中所有字节之和                (VPMADDUBSW→VPADDW→VPMADDWD)
weighted_sum_k    = 块 k 中 Σ (64−i)×byte_i            (VPMADDUBSW→VPMADDWD per half)
s1_after_k        = s1_before_k + delta_s1_k

s1 = Σ delta_s1_k                                       (Y14)
s2 = 64 × Σ s1_before_k + Σ weighted_sum_k              (Y4 = Σs1_before, Y12 = Σweighted)
```

**s1 归约** 使用 VPMADDWD 配合 int16 全 1 常量（Y11）：

```
VPMADDUBSW → VPADDW (合并两半) → VPMADDWD × int16_ones → 8×int32 delta_s1
```

一条指令替代 VPUNPCKLWD + VPUNPCKHWD + VPADDD（3→1）。可行是因为字节和不超过 signed int16 范围（<32767）。

**s2 加权归约** 每半使用 VPMADDWD（与 s1 相同的技巧）。每半的 int16 值 < 32767，所以 VPMADDWD pair-sum 安全。无需 VPADDW 合并——两半分别 VPMADDWD 后以 int32 合并。

### 2.2 初始值退出修正

`Y14` 仅跟踪**裸字节和**（init_s1 未广播）：

```
s1 = reduce(Y14) + init_s1
s2 = 64 × [reduce(Y4) + N × init_s1] + reduce(Y12) + init_s2
```

`N` = 64B 块数。`init_s1` 和 `init_s2` 从调用方指针读取。

### 2.3 CHAR_OFFSET 后修正（Go 层）

汇编计算裸字节和。Go 在之后加上 CHAR_OFFSET（`rolling_fast_amd64.go`）：

```go
// 私有 checksum1：asm 裸和 + Go CHAR_OFFSET
s1 += uint32(n) * CHAR_OFFSET
s2 += uint32(n) * uint32(n+1) / 2 * CHAR_OFFSET

// 公开 Checksum1：CHAR_OFFSET 在 asm 内处理（checksum1PackedAVX2）
```

### 2.4 剩余字节

汇编处理**全部**字节——完整 64B 块和标量剩余（0..63 字节）在主循环后的逐字节循环中处理。Go 侧无需再做剩余处理。

## 3. 循环结构

（19 条指令，交错 VPMADDUBSW）

```asm
loop:
    ; s1: VPMADDUBSW ×2 halves → VPADDW merge → VPMADDWD pair-sum
    VPMADDUBSW  Y15, Y2, Y0        ; 前 32B → 16 int16
    VPMADDUBSW  Y15, Y8, Y6        ; 后 32B → 16 int16
    VPADDW      Y6, Y0, Y0         ; 合并两半 (16-bit)
    VPMADDWD    Y11, Y0, Y0        ; pair-sum → 8×int32 delta_s1

    ; s2: Y4 += Y14（s1_before 累积——延迟）
    VPADDD      Y4, Y14, Y4

    ; s2: VPMADDUBSW × 权重表 → VPMADDWD per half → 以 int32 合并
    VPMADDUBSW  Y7, Y2, Y2         ; 前 32B × [64..33]
    VPMADDUBSW  Y13, Y8, Y3        ; 后 32B × [32..1]
    VPMADDWD    Y11, Y2, Y2        ; 前半 → 8 int32 pair-sums
    VPMADDWD    Y11, Y3, Y3        ; 后半 → 8 int32
    VPADDD      Y3, Y2, Y2         ; 合并两半 (32-bit)
    VPADDD      Y12, Y2, Y12       ; Y12 += weighted_sum

    PREFETCHT0  384(DI)            ; 提前预取 6 个 cacheline

    ; s1: Y14 += delta
    VPADDD      Y14, Y0, Y14

    ; 加载下一个块（先检查再加载，避免越界）
    SUBQ  $1, SI
    JZ    done
    VMOVDQU  0(DI), Y2             ; 下一个前 32B
    VMOVDQU  32(DI), Y8            ; 下一个后 32B
    ADDQ  $64, DI
    JMP   loop
done:
```

**关键设计决策：**

- **s1 和 s2 都用 VPMADDWD**：每半 int16 值 < 32767（s2 最大：64×255+63×255=32,385）。全程无需 VPUNPCK。
- **交错 VPMADDUBSW**：s1 先发射，s2 跟随——避免 4 条同时争抢端口 0/5。
- **PREFETCHT0**：Xeon 云虚拟机约 3% 增益，Zen 4 零成本。为兼容旧 CPU 保留。
- **底部加载 + 守卫**：`SUBQ/JZ` 在加载前判断，防止最后一次迭代越界读取。
- **合并退出归约**：Y4×64 + Y12 合并后一次水平求和（比分两次归约省约 5 条指令）。

## 4. Go Plan 9 汇编注意事项

### 4.1 VPMADDUBSW 操作数交换

| 来源 | src1 角色 | src2 角色 |
|--------|-----------|-----------|
| Intel 手册 | **unsigned** | **signed** |
| Go Plan 9 asm | **signed** | **unsigned** |

经诊断验证：`VPMADDUBSW data, ones` → data 被当作有符号；`VPMADDUBSW ones, data` → data 被当作无符号。

本项目的用法：`VPMADDUBSW Y15(ones=+1 signed), data(unsigned), dst` → 正确的无符号求和。

### 4.2 VPUNPCKLWD / VPUNPCKHWD lane 行为（历史——AVX2 路径已不再使用）

SSE2 路径仍使用 VPUNPCK 做位宽扩展。AVX2 路径中 VPMADDWD 已替代 s1 和 s2 的 VPUNPCK。

- `VPUNPCKLWD Y5(zero), Y0, Y3` — 将 16 个 int16 中偶数下标的 8 个零扩展到 8 个 int32，跨越两个 128 位 lane 无需 VEXTRACTI128。
- `VPUNPCKHWD Y5(zero), Y0, Y0` — 将奇数下标的 8 个零扩展到 8 个 int32。

两者组合（8+8=16）覆盖 VPMADDUBSW 的全部 16 个 int16 结果。

### 4.3 XMM/YMM 寄存器别名

`X0` 是 `Y0` 的**低 128 位**，不是独立寄存器。写 `Y0` 自动更新 `X0`。退出归约利用了这一点——无需 `VEXTRACTI128 $0, Y0, X0`。

### 4.4 VPANDN / VPTERNLOGD 操作数交换（MD5 核心）

Go Plan 9 对**所有**非交换 SIMD 指令交换 src1/src2：

| 指令 | Intel 手册 | Go Plan 9 asm |
|------------|-------------|---------------|
| `VPANDN A,B,C` | `C = ~A & B` | `C = A &^ B`（A & ~B） |
| `VPTERNLOGD imm,A,B,C` | n = (C<<2)\|(A<<1)\|B | n = (C<<2)\|(B<<1)\|A ← **交换** |

`VPTERNLOGD` 真值表立即数必须用 Go 交换后的顺序计算。使用 Intel 手册值（$0xB8/$0xCA/$0x65）会产生错误的 MD5 哈希。正确的 Go 交换值：R1=$0xD8, R2=$0xAC, R4=$0x63。参见 `gen_md5x8/main.go` 和 `gen_md5x16/main.go` 中已修正的生成器。

### 4.5 Go 汇编器限制

- VPMADDUBSW 不支持内存操作数（src2 必须是寄存器）。
- `VPBROADCASTD` 可用但曾是 bug 来源（见 §6.1）。
- 权重表必须用 `DATA /8` 配合 little-endian uint64 编码。

## 5. 演进历史

| 版本 | 关键变更 | 循环指令数 | Xeon 1KB | Ryzen 64KB |
|---------|-----------|:-----------:|:--------:|:----------:|
| v0（基线） | 有符号 VPMADDUBSW + VPMOVSXWD + 每迭代 s1 归约 | 45 | — | — |
| — | 无符号 + VPUNPCK 零扩展 | 41 | — | — |
| — | 预加载低位权重表 Y13 | 36 | — | — |
| — | s1 延迟归约 | 27 | — | — |
| v1 | 底部加载消除 Y9/Y10 + VPBROADCASTD 修复 | 28 | 27.2 GB/s | 51.5 GB/s |
| v2 | VPADDW 先合并再扩展（省 6 条指令） | 22 | 35.8 GB/s | 64.1 GB/s |
| v3 | PREFETCHT0 + 越界守卫（安全底部加载） | 22 | 36.6 GB/s | 59.6 GB/s |
| **v4** | **s1 用 VPMADDWD pair-sum**（省 2 条指令） | **20** | **—** | **69.2 GB/s** |
| v5 | **s2 每半用 VPMADDWD** + asm 标量剩余 + 合并退出归约 | **19** | 35.1 GB/s | — |
| **v6** | **CHAR_OFFSET + 打包在 asm 内**（`checksum1PackedAVX2`），合并 ones 表 | **19** | **37.4 GB/s** | — |

**累计**：28→19 条指令（−32%），Xeon 1KB 吞吐 +38%。64KB 与 rsync 差距在 1.4% 以内。

> **VPSRLD 死胡同**：尝试用 VPSRLD 做打包归约（3→2 条指令）。因高 16 位有垃圾数据导致 s1 放大 32768×。否决——`Roll()` 需要完整 32 位正确性。

## 6. 已知 Bug 及修复

### 6.1 VPBROADCASTD 放大（v0.1.x）

`VPBROADCASTD X0, Y14` 将 init_s1 复制到 8 个 lane。每次迭代 `Y4 += Y14` 将其计数了 8 倍。修复：保持 Y14 零初始化（仅跟踪裸字节和），退出时以标量应用 init_s1/s2（§2.2）。

### 6.2 Y15 寄存器污染（v0.1.3）

s2 权重加载段使用了 `LEAQ mul_T2<>+32(SB), AX; VMOVDQU (AX), Y15`，破坏了全 1 常量表。修复：使用独立的 Y13 加载低位权重。

### 6.3 VPANDN 操作数交换——AVX2 MD5（v0.1.4.2）

Go Plan 9 的 `VPANDN A,B,C` = `C = A &^ B`，而非 Intel 的 `C = ~A & B`。MD5 代码生成器（`gen_md5x8/main.go`）对 Round 1/2/4 使用了 Intel 语义，导致 F 函数全部错误。AVX2 MD5 的 8 个 lane 对每个块都静默算错。

**修复**：生成器中交换操作数 → 重新生成 `md5x8_amd64.s`。新增 `TestMD5x8_AVX2_Parity`（AVX2 vs stdlib md5.Sum）。

### 6.4 VPGATHERDD 掩码清零 + Go asm VSIB bug（v0.1.4.2–v0.1.4.3）

gather 加载路径的两个 bug：
1. **掩码清零**——VPGATHERDD 执行后清零掩码寄存器（Intel 规范）。只初始化一次掩码 → 只有第一次 gather 有效。
2. **VSIB 编码**——Go Plan 9 汇编器对 VPGATHERDD 编码有基址寄存器硬编码和位移量错误。

**修复**：每次 gather 前重载掩码（`VPCMPEQD` / `KXNORW`）。用原始机器码 `BYTE` 操作码完全绕过 Go asm 的 VSIB bug。

### 6.5 VPTERNLOGD 操作数交换——AVX-512 MD5（v0.1.4.4）

与 §6.3 同类：Go Plan 9 对 `VPTERNLOGD` 交换 src1/src2。真值表索引为 `n=(dst<<2)|(src2<<1)|src1`（非 Intel 的 src1/src2 顺序）。AVX-512 核心中使用 VPTERNLOGD 的三个轮次（R1、R2、R4）立即数全部错误。

**影响**：1GB 相同文件同步耗时 2 分钟以上——服务端（Xeon，AVX-512）生成错误 MD5 签名，客户端（stdlib MD5）找不到任何匹配 → 逐字节扫描 10 亿个位置。

**修复**：按 Go 交换顺序重新计算全部 imm8 值（$0xD8/$0xAC/$0x63），重新生成 `md5x16_amd64.s`。新增 `TestMD5x16_AVX512_Parity`、`TestMD5x16_CoreOnly`、`TestMD5x16_GatherVerification`。

## 7. 寄存器映射

| 寄存器 | 用途 | 生命周期 |
|----------|---------|----------|
| Y15 | 全 1 表（0x01 × 32）用于 VPMADDUBSW | 常量 |
| Y11 | int16 全 1（0x0001 × 16）用于 VPMADDWD | 常量 |
| Y7 | 权重表 [64..33] | 常量 |
| Y13 | 权重表 [32..1] | 常量 |
| Y2 | 当前 64B 块，前 32B | 每次迭代 |
| Y8 | 当前 64B 块，后 32B | 每次迭代 |
| Y0 | 临时（s1 delta via VPMADDWD） | 每次迭代 |
| Y3 | s2 后半 | 每次迭代 |
| Y6 | 临时（s1/s2 后半） | 每次迭代 |
| Y14 | 累积 s1（向量，仅裸字节和） | 跨迭代 |
| Y4 | Σ s1_before_k（延迟 s2） | 跨迭代 |
| Y12 | Σ 加权字节和（延迟 s2） | 跨迭代 |
| DI | 数据指针 | 跨迭代 |
| SI | 迭代计数器 | 跨迭代 |
| R13 | init_s1（退出时使用） | 函数生命周期 |
| DX | init_s2（退出时使用） | 函数生命周期 |
| R15 | original_len（剩余处理用） | 函数生命周期 |
| R12 | N = 迭代次数（退出修正用） | 函数生命周期 |
| R10 | 退出：s1 标量 | 仅退出 |
| R9, R11 | 退出：s2 归约临时 | 仅退出 |

未使用 YMM 寄存器：Y1、Y5、Y9、Y10。

## 8. 测试覆盖

### 校验和 parity 测试（`avx2_test.go`）

对比 AVX2 和 SSE2 输出与逐字节参考值（无 CHAR_OFFSET）：

| 测试 | 数据 | 目的 |
|------|------|---------|
| `TestAVX2Parity`（11 组） | zeros, 0xFF, 递增, 随机 | 验证 AVX2 引擎 |
| `TestSSE2Parity`（10 组） | zeros, 0xFF, 递增, 随机 | 验证 SSE2 引擎 |

### MD5 SIMD parity 测试（`md5x8_test.go`）

对比 AVX2/AVX-512 MD5 输出与 stdlib `md5.Sum`：

| 测试 | 范围 |
|------|-------|
| `TestMD5x8_AVX2_Parity` | 8 路 AVX2 MD5 vs stdlib（700 字节块） |
| `TestMD5x16_AVX512_Parity` | 16 路 AVX-512 MD5 vs stdlib（2048 字节块） |
| `TestMD5x16_UnevenLengths` | 16 个不等长块（63–4096 字节） |
| `TestMD5x16_CoreOnly` | AVX-512 核心 + 手动构造 x 矩阵（绕过 gather） |
| `TestMD5x16_GatherVerification` | 验证 VPGATHERDD 加载正确的转置数据 |

### 性能基准（`tier_bench_test.go`）

三路对比：

| 基准 | 引擎 |
|-----------|--------|
| `SSE2/*KB` | `checksum1SSE2`（32B/iter, XMM） |
| `AVX2/*KB` | `checksum1AVX2`（64B/iter, YMM） |
| `Go/*KB` | 纯 Go 128B 批次（无 SIMD） |

### 集成测试（`delta_test.go`）

端到端 delta 往返、相同文件、示例用法。

## 9. 性能数据

**Intel Xeon Platinum 云虚拟机（2 vCPU，约 2.5 GHz）：**

| 块大小 | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------------|:-----------:|:-----------:|:----------:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 8 KB | **42.8 GB/s** | — | 48.3 GB/s |
| 64 KB | **43.7 GB/s** | 26.7 GB/s | 44.3 GB/s |
| 1 MB | **43.6 GB/s** | 42.4 GB/s | — |

**AMD Ryzen 9 8940HX（Zen 4，笔记本）：**

| 块大小 | go-rsync v6 | v1（基线） | 提升 |
|------------|:-----------:|:-------------:|:-----------:|
| 1 KB | 55.1 GB/s | 44.8 GB/s | +23% |
| 64 KB | **69.2 GB/s** | 51.5 GB/s | **+34%** |
| 1 MB | 51.2 GB/s | 51.2 GB/s | — |

**跨平台三级对比（Ryzen 9, 64KB）：**

| 级别 | 吞吐量 | vs AVX2 |
|------|:----------:|:-------:|
| AVX2（64B/iter） | 69.2 GB/s | — |
| SSE2（32B/iter） | 26.1 GB/s | 慢 2.7× |
| 纯 Go（128B batch） | 1.9 GB/s | 慢 36× |

## 10. 附录：SSE2 路径

SSE2 路径不是 AVX2 的简单机械翻译。关键差异：

| 方面 | AVX2 | SSE2 | 原因 |
|--------|------|------|--------|
| s1 归约 | VPMADDWD pair-sum | **VPHADDW** pair-sum | Go asm 缺少 XMM 版 `VPADDW`；VPHADDW 是唯一可用的 XMM 字加法 |
| s2 归约 | VPADDW 合并 + VPUNPCK | VPUNPCK per-half（不合并） | XMM 不能用 VPADDW；每半各自扩展 |
| 块大小 | 64B/iter | 32B/iter | XMM = 128 位 |
| `VPMADDWD` | YMM, int16_ones 表 | 未使用 | VPMADDWD 可用但无法先合并 |
| `PREFETCHT0` | 384(DI) | 384(DI) | 相同 |
| 循环指令数 | 20 | ~22 | s2 每半多了 2 条 VPUNPCK |

**VPBROADCASTD bug（v0.2.1 修复）**：原始 SSE2 代码将 `init_s1` 广播到 4 个 XMM lane，导致 4 倍放大。修复：保持 X14 零初始化，退出时以标量应用 init_s1（与 AVX2 相同方案）。

**XMM PADDW 限制**：Go Plan 9 汇编器仅对 YMM 寄存器定义了 `APADDW`（`{APADDW, ymm, Py1, ...}`），没有 XMM 变体。这导致 SSE2 无法使用 VPADDW 先合并再扩展的优化，每次迭代多消耗约 2 条指令。

## 11. 附录：逐尺寸性能数据

**测试环境**：同一台 Xeon Platinum 云虚拟机，相同数据模式（`i*7%251`），均包含完整尾部字节处理。go-rsync 通过 `Checksum1()` 自动分发到 AVX2。测量误差 ±3%。

| 大小 | go-rsync v6 | go-rsync v4 | rsync-AVX2 |
|------|:---:|:---:|:---:|
| 1 KB | **37.4 GB/s** | 16.8 GB/s | 43.4 GB/s |
| 4 KB | — | 36.8 GB/s | 48.3 GB/s |
| 16 KB | — | 39.2 GB/s | 49.0 GB/s |
| 64 KB | **43.7 GB/s** | 40.7 GB/s | 44.3 GB/s |
| 97 KB | — | 41.1 GB/s | 44.8 GB/s |
| 128 KB | — | 41.3 GB/s | 45.1 GB/s |
| 256 KB | — | 41.5 GB/s | 45.2 GB/s |

> v6 将 1KB 场景相对 rsync 的差距从 −62%（v4）缩小到 −14%。64KB 现在与 rsync 差距在 1.4% 以内。剩余的 1KB 差距源于 rsync 的 VPSRLD/VPSRLDQ + 退出修正方案，牺牲代码清晰度换取了 Xeon 上约 15% 更多的端口吞吐。

---

> 相关文档：[MD5 SIMD 参考](md5-simd.md) · [项目 README](../README.md)
