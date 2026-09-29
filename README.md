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

| 包 | 职责 |
|---|---|
| `xconstraints` | 泛型约束（`Number` / `Integer` / `Ordered` …） |
| `xerr` | 错误码、包装、分类、聚合 |
| `xsafe` | panic 安全（`Go` / `Recover` / `Caller`） |
| `xcontext` | 类型安全的 context 值传递 |
| `xreflect` | 深拷贝、字段元数据缓存、弱类型赋值 |
| `xtype` | 泛型类型操作（取地址 / 算术 / 比较 / 条件） |
| `xstr` | 字符串工具 |
| `xnum` | 数值与精度（`Decimal`） |
| `xtime` | 时间工具 |
| `xmap` | map 算法 |
| `xslice` | slice 算法 |
| `xconv` | 类型转换 |
| `xtest` | 测试工具 |
| `xcontainer/xstack` | 栈 |
| `xcontainer/xqueue` | 队列（切片 / 环形 / 有界） |

## 快速开始

```go
package main

import (
	"fmt"

	"github.com/zhangwenxing/gokit/xconv"
	"github.com/zhangwenxing/gokit/xslice"
	"github.com/zhangwenxing/gokit/xtype"
)

func main() {
	in := []int{3, 1, 3, 2, 1}
	fmt.Println(xslice.Dedup(in))                       // [3 1 2]
	fmt.Println(xconv.SliceToString(xslice.Sort(in), ",")) // 1,2,3
	fmt.Println(*xtype.Ptr("hello"))                    // hello
}
```

## 开发

```bash
export PATH=/opt/go_1.21/go/bin:$PATH
export GOTOOLCHAIN=local

make test      # go test -race ./...
make cover     # 覆盖率报告
make lint      # gofmt + go vet
make check     # build + vet + test + cover
```

## 约定

- **永不 panic**：除 `MustXxx` 变体外，`nil` 输入返回零值或 error。
- **Options 模式**：`WithXxx()` 函数式选项。
- **并发语义显式**：类型 godoc 首行标注「并发安全 / 非并发安全」。
- **不可变优先**：返回新集合；需要原地修改的提供 `XxxInPlace`。
- 每个包 godoc 注释 + 表驱动测试 + 边界用例（`nil` / 空 / 单元素 / 重复 / 并发）。
