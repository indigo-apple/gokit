# gokit

Go 通用工具链 —— 泛型优先、零第三方依赖（核心包 100% 标准库）。

设计文档见 [DESIGN.md](./DESIGN.md)。

## 要求

- Go **1.21+**（依赖泛型、`log/slog`、`slices`/`maps`、内置 `min`/`max`/`clear`、`context.AfterFunc`、`context.WithoutCancel`）

## 安装

```bash
go get github.com/zhangwenxing/gokit
```

核心包无任何 `require`，不会污染你的依赖树。

## 包索引

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xconstraints` | 泛型约束（`Number` / `Integer` / `Ordered` / `Key`） | 纯类型 |
| `xerr` | 错误码、包装、分类判定、`MultiError` 聚合 | 87% |
| `xsafe` | panic 安全（`Go` / `Recover` / `Try` / `Caller`） | 97% |
| `xcontext` | 类型安全的 context 键、`Merge` / `Detach`、HTTP 链路提取 | 94% |
| `xreflect` | 深拷贝（含循环引用）、字段元数据缓存、**弱类型赋值**、struct↔map | 81% |
| `xtype` | 泛型类型操作（取地址 / 算术 / 比较 / 条件） | 99% |
| `xstr` | 字符串（命名风格 / 截断 / 脱敏 / 随机串 / 相似度） | 95% |
| `xnum` | 数值（`Decimal` 精度 / 字节单位 / 进制 / 格式化） | 90% |
| `xtime` | 时间（边界 / 解析 / 人类可读文案 / Stopwatch） | 98% |
| `xmap` | map 算法（合并 / 集合运算 / **稳定遍历** / 嵌套路径） | 93% |
| `xslice` | slice 算法（去重 / 变换 / 分组 / 集合运算 / 排序） | 97% |
| `xconv` | **类型转换枢纽**（容错链式取值 / map↔slice↔string↔struct↔JSON） | 91% |

## 依赖分层

```
xconstraints ──┐
               ├─> xtype
xerr ──────────┼─> xreflect ──> xmap ──> xconv
xtime ─────────┘        │        ↑
xsafe  xcontext         └─> xslice ┘
xstr   xnum  (xconv 复用 xnum)
```

- `xreflect.Assign` 是**全工具链唯一的弱类型转换底座**，`xconv` 与 `xmap` 都建立在它之上，
  因此不存在「两套转换规则」。
- `xtime.Layouts` 是**唯一的时间布局嗅探列表**，`xreflect` 复用同一份，
  保证 `MapToStruct` 与 `xtime.Parse` 对同一字符串解析一致。

## 快速开始

```go
package main

import (
	"fmt"

	"github.com/zhangwenxing/gokit/xconv"
	"github.com/zhangwenxing/gokit/xnum"
	"github.com/zhangwenxing/gokit/xslice"
)

func main() {
	// 切片：去重 + 排序
	in := []int{3, 1, 3, 2, 1}
	fmt.Println(xslice.SortOrdered(xslice.Dedup(in))) // [1 2 3]

	// 转换：map → slice → string
	m := map[string]int{"b": 2, "a": 1}
	fmt.Println(xconv.SliceToString(
		xconv.MapToSliceSorted(m, func(k string, v int) string { return k }), ",")) // a,b

	// 容错链式取值（配置场景）
	cfg := map[string]any{"db": map[string]any{"pool": map[string]any{"max": 20}}}
	fmt.Println(xconv.V(cfg).Path("db.pool.max").Int(5)) // 20

	// 金额精度：float64 会得到 0.30000000000000004
	fmt.Println(xnum.NewDecimal("0.1").Add(xnum.NewDecimal("0.2"))) // 0.3
}
```

## 开发

```bash
export PATH=/opt/go_1.21/go/bin:$PATH
export GOTOOLCHAIN=local

make test      # go test -race ./...
make cover     # 覆盖率报告
make lint      # gofmt + go vet
make fuzz-short # 短时模糊测试
make check     # build + vet + fmt-check + test
```

## 测试与质量

- `go vet` 零告警，`-race` 全绿，linux/darwin/windows × amd64/arm64 交叉编译通过
- 所有包均为**表驱动测试 + 边界用例**（`nil` / 空 / 单元素 / 重复 / 并发）
- 解析与算法类函数配有**模糊测试**（已跑通百万级执行）：
  - `xconv`: `FuzzToInt`、`FuzzIntToBaseRoundTrip`（进制往返不变式）、`FuzzValueChain`
  - `xslice`: `FuzzDedup`、`FuzzSetOperations`、`FuzzRotateAndWindow`

## 约定

- **永不 panic**：除 `MustXxx` 变体外，`nil` 输入返回零值或 error
- **Options 模式**：`WithXxx()` 函数式选项
- **并发语义显式**：类型 godoc 首行标注「并发安全 / 非并发安全」
- **不可变优先**：返回新集合；需要原地修改的显式以 `InPlace` 结尾
- **失败可区分**：转换类函数提供 `CastErr` / `ToInt64` 等带 error 的变体，
  以及 `V(x).Int(def)` 这样的容错链
