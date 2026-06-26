# go-rsync MD5 SIMD 参考

> AVX2（8 路）+ AVX-512（16 路）并行 MD5 的设计、技术细节、注意事项和调试记录。

## 目录

- [1. 架构概览](#1-架构概览)
- [2. 核心文件](#2-核心文件)
- [3. 技术要点](#3-技术要点)
- [4. 重新生成](#4-重新生成)
- [5. 安全检查清单](#5-安全检查清单)
- [6. Go Plan 9 汇编注意事项](#6-go-plan-9-汇编注意事项)
- [7. 调试日志（2026-06-26）](#7-调试日志2026-06-26)
- [8. 经验教训](#8-经验教训)

## 1. 架构概览

```
GenerateSignature(data, blockSize) → GenerateSignatureReader(io.Reader, ...)
│
├─ Checksum1 (weak) ──────────────────────────────────────────
│   ├─ [amd64] AVX2 64B/iter  → rolling_amd64.s
│   ├─ [amd64] SSE2 32B/iter  → rolling_sse2_amd64.s
│   └─ [!amd64] byte loop     → rolling_generic.go
│
└─ strong hash (MD5) ─────────────────────────────────────────
    ├─ Phase 1: N 个完整 64B 块，批量 SIMD
    │     load+gather → md5x8core (或 md5x16core)
    ├─ Phase 2: 尾部 + 填充（md5Finalize8way / 标量回退）
    └─ [!amd64] crypto/md5 桩

路径选择：AVX-512（blockSize ≥ 2KB）→ AVX2 → 标量
```

## 2. 核心文件

| 文件 | 用途 |
|------|------|
| `md5x8_amd64.s` | 8 路 AVX2 核心，**由 `gen_md5x8/main.go` 生成** |
| `md5x8_gather_amd64.s` | **原始机器码** VPGATHERDD 加载+转置（BYTE 操作码） |
| `md5x8_load_transpose_amd64.s` | VPINSRD 标量回退（~288 指令/块，始终正确） |
| `md5x8_purego.go` | 纯 Go 8 路 MD5 参考实现（用于验证 & 非 AVX2 回退） |
| `md5x8_transpose.s` | 连续 8×64→16 YMM 转置（仅尾部） |
| `md5x8_transpose_fast_amd64.s` | 寄存器 shuffle 转置（~80 vs ~320 条 VPINSRD） |
| `md5x8_amd64.go` | 胶水代码：`md5Hash8wayAVX2`、终结处理 |
| `md5x8_common.go` | 共享常量 `t256[]`、`shifts[]`、`md5FinalLane` |
| `gen_md5x8/main.go` | AVX2 核心代码生成器 |
| `md5x16_amd64.s` | 16 路 AVX-512 核心，**由 `gen_md5x16/main.go` 生成** |
| `md5x16_gather_amd64.s` | ZMM VPGATHERDD（k-mask + GPR 索引） |
| `md5x16_amd64.go` | 胶水代码：`md5Hash16wayAVX512` |
| `gen_md5x16/main.go` | AVX-512 核心代码生成器 |
| `md5x8_test.go` | 测试：8 路 + 16 路 MD5 parity、gather 验证 |

## 3. 技术要点

### 数据布局

块按顺序打包到批量缓冲区（8× 或 16× blockSize）。每个 64B 块转置为 SoA：字 `w` 成为一个包含 8/16 个 lane 的 YMM/ZMM。核心通过单条对齐 `VMOVDQU` 读取 X[g]。

### 加载

`VPGATHERDD` 一条指令从分散的 8（或 16）个位置收集 dword。

- **AVX2**：使用 YMM 掩码（`VPCMPEQD Y,Y,Y` 产生全 1）
- **AVX-512**：使用 k-mask（`KXNORW K1,K1,K1`）

### ⚠️ VPGATHERDD 掩码清零

VPGATHERDD 执行后**清零掩码寄存器**（Intel 规范）。必须在**每次** gather 前重载掩码：在空闲寄存器中预计算全 1（如 `VPCMPEQD Y3,Y3,Y3`），然后在每次 gather 前 `VMOVDQA Y3,Y2`。不重载的话后续 gather 会静默读取零数据。

### ⚠️ Go 汇编器 VSIB bug

Go Plan 9 的 VPGATHERDD 编码有多处错误：基址寄存器被硬编码（运行时修改 R8/R12/BP 无效）、VSIB 位移量字段产生错误地址、非 Y1 目标寄存器可能返回零。绕过方案：通过 `BYTE` 伪指令直接写入原始机器码。

`VPGATHERDD Y2, disp(R8)(Y7*2), Y1` 的编码：
```
C4 C2 6D 90 [ModRM] 78 [disp8]
    ↑ VEX3   ↑ opcode  ↑ VSIB (scale=×2,idx=Y7,base=R8)
```
字 0（无位移）：`BYTE $0xC4; BYTE $0xC2; BYTE $0x6D; BYTE $0x90; BYTE $0x0C; BYTE $0x78`
字 N（位移=N*4）：同上但 ModRM=`4C` + 末尾 `BYTE $disp`

### 隐式寄存器轮转

Y0-Y3 保存 (a,b,c,d)，但逻辑角色通过编译期表格每步轮转，避免每条指令 5 次 `VMOVDQA`。只有一次写入：`VPADDD rb, temp, ra`。AVX-512 中 ZMM 同理。

### 合并 T 常量加载

`VPADDD T_i<>(SB), Y12, Y12`（内存源加法）替代 `VMOVDQU + VPADDD`。常量存储为 8×/16× 膨胀的 DATA 表。

### 软件流水线（X 预取）

在步骤 `i` 的**末尾**加载 `X[g_{i+1}]`（与写回重叠），而非在关键路径上的步骤开始处加载 `X[g_i]`。浅 OoO 窗口 CPU 上增益显著（Zen 4 上 +67%）；深窗口 Xeon 上可忽略。

### Save-add

MD5 要求每个块后 `digest += initial`。初始状态在入口保存到 Y4-Y7（Z4-Z7），存储前加回。

### AVX-512 µop 优势

`VPTERNLOGD` 用 1 条指令完成 F 函数（AVX2 需 3 条）；`VPROLD` 用 1 条指令完成循环移位（AVX2 需 3 条）。配合 2 倍 lane 数，理论吞吐量约 3 倍。

### 阈值

AVX-512 gather 开销对小块不利。`blockSize ≥ 2048` 时启用 AVX-512；更小的使用 AVX2。

## 4. 重新生成

```bash
cd gen_md5x8 && go run .    # → ../md5x8_amd64.s
cd gen_md5x16 && go run .   # → ../md5x16_amd64.s
```

## 5. 安全检查清单

修改汇编后逐项确认：

- [ ] `go vet .` — 零警告
- [ ] `go test -count=1 .` — 全部测试通过（含 AVX-512 parity，如果 CPU 支持）
- [ ] 所有 YMM/ZMM 函数的 `RET` 前有 `VZEROUPPER`
- [ ] VPGATHERDD 在**每次** gather 前重载掩码（指令会清零掩码）
- [ ] VPTERNLOGD 立即数使用 Go 交换后的操作数顺序（非 Intel 手册值）
- [ ] 所有含指针参数的 asm 声明有 `//go:noescape`
- [ ] Slice 参数在栈帧中占 24 字节（ptr+len+cap），非 16
- [ ] 非 amd64 编译通过：`GOOS=darwin GOARCH=arm64 go build .`

## 6. Go Plan 9 汇编注意事项

### VPANDN 操作数顺序

Go Plan 9 的 `VPANDN A,B,C` = `C = A &^ B`（非 Intel 的 `C = ~A & B`）。生成器 `gen_md5x8/main.go` 已修正。

### VPTERNLOGD 操作数顺序

Go Plan 9 的 `VPTERNLOGD imm,src1,src2,dst` 计算真值表索引为 `n = (dst<<2)|(src2<<1)|src1`，**非** Intel 的 `n = (dst<<2)|(src1<<1)|src2`。与 VPANDN 相同的操作数交换。AVX-512 生成器 `gen_md5x16/main.go` 使用 Go 交换后的立即数（R1=$0xD8, R2=$0xAC, R4=$0x63）。**禁止使用 Intel 手册值**。

### VPGATHERDD 语法

Go asm 顺序为 `(base)(idx*scale), mask, dst`——非 Intel 的 `mask, mem, dst`。AVX-512 需要 `K1`（k-mask），非 `YMM`。

### VPGATHERDD 掩码初始化

必须显式设为全 1（YMM 用 `VPCMPEQD Y,Y,Y`；k-mask 用 `KXNORW K1,K1,K1`）。未初始化的掩码静默填零。

### VPGATHERDD 原始机器码

Go 汇编器 VSIB 编码有 bug（基址寄存器硬编码、位移量错误）。绕过方案：`BYTE` 操作码（编码见 §3 Loading 节）。

### VPMADDUBSW 操作数顺序

Go asm：有符号在前，无符号在后。Intel 手册顺序相反。

### 杂项

- **PowerShell 编码**：`Set-Content` → UTF-16。使用编辑器编辑 `.s` 文件，不要用 shell 重定向。
- **交叉编译**：`$env:GOOS` 在 PowerShell 中持久保留。交叉编译后用 `$env:GOOS=""` 清除。

## 7. 调试日志（2026-06-26）

> 完整记录 MD5 SIMD 的调试过程，作为未来 Go Plan 9 汇编排错的参考。

### 7.1 症状：相同文件产生 98% 的字面量传输

`TestDeltaIdentical` 显示 `LiteralBytes = 50500/51200`（98.63%），而非预期的 `<700`。Search 对相同数据几乎找不到匹配。

### 7.2 弱校验和 vs 强校验和不匹配

`Checksum1()` 和 `RollingSum.Value()` 对所有块结果一致——弱校验和路径正确。但 `sig.Sum2`（存储的 MD5）≠ `md5.Sum()`——两条路径产生了不同的强哈希值。AVX2 MD5 核心在生成错误的签名，因此 `computeStrong()`（标准 `md5.New()`）永远无法匹配。

### 7.3 根因：VPANDN 操作数顺序（v0.1.4.2）

Go Plan 9 `VPANDN A,B,C` = `C = A &^ B`（Plan 9 语义），非 Intel 的 `C = ~A & B`。

代码生成器 `gen_md5x8/main.go` 按 Intel 语义编写，使用 VPANDN 的三个轮次（R1、R2、R4）全部产生错误的 F 函数。

**修复**：生成器中交换操作数 → 重新生成 `md5x8_amd64.s`。新增 `TestMD5x8_AVX2_Parity` 验证。

### 7.4 次生 bug：VPGATHERDD 掩码清零（v0.1.4.2–v0.1.4.3）

VPGATHERDD 执行后清零掩码寄存器。gather 函数只设置了一次全 1 掩码，然后运行 16 次 gather——只有第一次有效，后续全部读到零数据。

**修复**：在空闲寄存器中预计算全 1（`VPCMPEQD Y3,Y3,Y3`），每次 gather 前通过 `VMOVDQA Y3,Y2` 重载掩码。

### 7.5 三级 bug：Go 汇编器 VSIB 编码（v0.1.4.2）

Go Plan 9 的 VPGATHERDD 汇编编码有多个问题：
- 基址寄存器：硬编码为首次遇到的值，运行时修改 R8/R12/BP 无效
- VSIB 位移量：产生错误地址
- 非 Y1 目标寄存器：可能返回零数据
- 缩放因子 ≠2：可能编码不正确

经过穷举测试确认（scale=1/2/4 × byte/dword/word 偏移 × R8/R10/R12/BP 基址 × LEAQ/ADDQ 基址修改）。

**修复**：通过 `BYTE` 伪指令直接写入原始机器码，完全绕过 Go 汇编器处理 VPGATHERDD。

### 7.6 AVX-512：VPTERNLOGD 避免了 VPANDN，同样的掩码清零问题（v0.1.4.3）

AVX-512 使用 `VPTERNLOGD` 做轮函数（无 VPANDN），核心本身不受影响。但 `md5x16_gather_amd64.s` 存在同样的掩码清零 bug，外加无法编译的 `#define` 宏（Go asm 不支持 C 预处理器）。

**修复**：手动展开 WORD16 宏为 DATA/GLOBL 声明；每次 VPGATHERDD 前添加 `KXNORW K1,K1,K1`。

### 7.7 AVX-512：VPTERNLOGD 操作数交换（v0.1.4.4）❗ 关键

Go Plan 9 的 `VPTERNLOGD` 交换 src1/src2，与 VPANDN 相同的 bug：

```
Intel:  n = (dst<<2)|(src1<<1)|src2     (dst=zmm1, src1=zmm2, src2=zmm3)
Go asm: n = (dst<<2)|(src2<<1)|src1     ← SWAPPED!
```

之所以长时间未被发现：
- AVX2 测试通过（AVX2 使用 VPANDN，不用 VPTERNLOGD）
- 没有 AVX-512 parity 测试（本地 Ryzen 9 支持 AVX-512 但测试使用 blockSize=700，小于 AVX-512 阈值 2048）
- 旧的立即数（R2=$0xE2, R4=$0xD9）碰巧通过了 `TestMD5x16_UnevenLengths`，因为 minFullChunks=0 → 走标量回退，完全未执行 VPTERNLOGD

**正确的 Go Plan 9 VPTERNLOGD 立即数**：

| 轮次 | Intel 手册 | Go Plan 9（正确） | 旧值（错误） |
|-------|-------------|---------------------|--------------|
| R1    | $0xB8       | **$0xD8**           | $0xB8        |
| R2    | $0xCA       | **$0xAC**           | $0xE2        |
| R4    | $0x65       | **$0x63**           | $0xD9        |

R3 使用 VPXOR（无操作数顺序问题，始终正确）。

**影响**：1GB 相同文件同步耗时 2 分钟以上——服务端（Xeon，AVX-512）生成错误 MD5 签名，客户端（stdlib MD5）找不到任何匹配 → 逐字节扫描 10 亿个位置。

**修复**：重新计算 Go 交换顺序的 imm8 值，重新生成 `md5x16_amd64.s`。新增 3 个 AVX-512 parity 测试防止回归。

## 8. 经验教训

1. **用标准库验证汇编。** `md5.Sum()` 是真值。`TestMD5x8_AVX2_Parity` 和 `TestMD5x16_AVX512_Parity` 现在守卫所有回归。
2. **测试每个代码路径。** AVX-512 parity 数周未被测试，因为测试默认的 blockSize=700 低于 AVX-512 阈值（2048）。`TestMD5x16_*` 测试现在显式使用大块。
3. **VPGATHERDD 清零掩码。** 永远在每次 gather 前重载掩码。
4. **Go Plan 9 操作数顺序 ≠ Intel。** 写大规模汇编函数前务必用小测试验证。我们已在 `VPMADDUBSW`、`VPANDN` 和 `VPTERNLOGD` 上三次踩坑。
5. **`BYTE` 原始机器码是可行的逃生舱**，当汇编器有编码 bug 时。机器码格式稳定，汇编器不稳定。
6. **纯 Go 参考实现对排错至关重要。** `md5x8_purego.go` 对隔离每个 bug 发挥了关键作用。

---

> 相关文档：[校验和引擎](checksum-engine.md) · [项目 README](../README.md)
