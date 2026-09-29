package xtest

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// ---------------------------------------------------------------------------
// 更新模式
// ---------------------------------------------------------------------------

// 更新模式的三种开启方式（按优先级）：
//
//  1. 代码内显式调用 xtest.SetUpdate(true)；
//  2. 环境变量 GOKIT_UPDATE=1 / XTEST_UPDATE=1（**零配置，推荐用于一次性生成**）；
//  3. 命令行 -update（需要先由使用方声明，见 DeclareUpdateFlag）。
//
// 设计取舍：本包**不**在 init() 里注册 -update 标志。
// 因为测试二进制中同名标志会被 flag.Bool 判定为重复注册而 panic，
// 若使用方自己也定义了 -update（golden 测试的常见做法），就会在启动时崩溃。
// 因此改为「按需声明 + 自动识别已有标志」。
var programmaticUpdate bool

// SetUpdate 以代码方式开启 / 关闭 golden 更新模式。
//
// 注意：在测试函数体内调用它只能影响之后的调用；要影响整个测试二进制，
// 请在 TestMain 中调用。
func SetUpdate(v bool) { programmaticUpdate = v }

// DeclareUpdateFlag 声明 -update 命令行标志（若已存在同名标志则复用，不重复注册）。
//
// 在 TestMain 或包级 init 中调用一次即可，之后 `go test -update ./...` 就能更新 golden 文件：
//
//	func TestMain(m *testing.M) {
//		xtest.DeclareUpdateFlag()
//		os.Exit(m.Run())
//	}
func DeclareUpdateFlag() {
	if flag.Lookup("update") != nil {
		return
	}
	// 注册失败（如 flag 已解析）时忽略：此时退化为环境变量方式。
	defer func() { _ = recover() }()
	flag.Bool("update", false, "更新 xtest golden 文件")
}

// Updating 返回当前是否处于 golden 更新模式。
func Updating() bool {
	if programmaticUpdate {
		return true
	}
	if truthy(os.Getenv("GOKIT_UPDATE")) || truthy(os.Getenv("XTEST_UPDATE")) {
		return true
	}
	// 自动识别使用方自己声明的 -update 或本包 DeclareUpdateFlag 注册的 -update
	if f := flag.Lookup("update"); f != nil {
		return truthy(f.Value.String())
	}
	return false
}

// truthy 判断环境变量 / 标志值是否表示「真」。
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// golden 文件
// ---------------------------------------------------------------------------

// Golden 把 got 与 golden 文件比对。
//
//   - 处于更新模式（见 Updating）时：写入 / 覆盖 path，并记录日志；
//   - 否则：读取 path 并逐字节比较，不一致时输出差异摘要；
//   - golden 文件不存在时给出可直接执行的处理建议。
//
// path 相对路径的基准是测试进程的工作目录（即被测包目录），
// 惯例是把 golden 文件放在 `testdata/` 下（Go 工具链会忽略该目录）。
func Golden(t TestingT, path string, got []byte) {
	t.Helper()
	if strings.TrimSpace(path) == "" {
		t.Fatalf("xtest.Golden: path 为空")
		return
	}

	if Updating() {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("xtest.Golden: 创建目录 %s 失败: %v", dir, err)
				return
			}
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("xtest.Golden: 写入 golden 文件失败: %v", err)
			return
		}
		t.Logf("xtest.Golden: 已更新 %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("xtest.Golden: golden 文件不存在: %s\n"+
				"  生成方式：GOKIT_UPDATE=1 go test ./...（或声明 -update 后 go test -update ./...）", path)
			return
		}
		t.Fatalf("xtest.Golden: 读取 golden 文件失败: %v", err)
		return
	}
	if bytes.Equal(want, got) {
		return
	}

	// 二进制内容不做行差分
	if !isText(want) || !isText(got) {
		t.Fatalf("xtest.Golden: 输出与 %s 不一致（二进制，%d vs %d 字节）", path, len(want), len(got))
		return
	}
	t.Fatalf("xtest.Golden: 输出与 %s 不一致\n%s", path, DiffText(string(want), string(got)))
}

// isText 粗略判断字节内容是否为文本（用于决定是否输出行差分）。
func isText(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	return bytes.IndexByte(b[:n], 0) < 0
}

// GoldenJSON 把 got 序列化为缩进 JSON 后与 golden 文件比对。
//
// 序列化固定使用两个空格缩进并追加换行，保证 diff 稳定、便于人工审阅。
func GoldenJSON(t TestingT, path string, got any) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("xtest.GoldenJSON: 序列化失败: %v", err)
		return
	}
	Golden(t, path, append(b, '\n'))
}

// ---------------------------------------------------------------------------
// JSON 断言
// ---------------------------------------------------------------------------

// canonJSON 把「对象 / 字符串 / 字节切片」统一解析为 any，便于语义比较。
func canonJSON(v any) (any, error) {
	var raw []byte
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// JSONEqual 语义比较两段 JSON：忽略键顺序、空白与转义差异。
//
// 入参可以是对象、JSON 字符串或 JSON 字节切片。任一入参非法时返回 false。
func JSONEqual(want, got any) bool {
	a, err1 := canonJSON(want)
	if err1 != nil {
		return false
	}
	b, err2 := canonJSON(got)
	if err2 != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// EqualJSON 断言两段 JSON 语义相等（忽略键顺序与空白）。
func EqualJSON(t TestingT, want, got any) {
	t.Helper()
	a, err1 := canonJSON(want)
	if err1 != nil {
		t.Fatalf("xtest.EqualJSON: want 不是合法 JSON: %v", err1)
		return
	}
	b, err2 := canonJSON(got)
	if err2 != nil {
		t.Fatalf("xtest.EqualJSON: got 不是合法 JSON: %v", err2)
		return
	}
	if reflect.DeepEqual(a, b) {
		return
	}
	wj, _ := json.MarshalIndent(a, "", "  ")
	gj, _ := json.MarshalIndent(b, "", "  ")
	t.Fatalf("xtest.EqualJSON 不相等\nwant:\n%s\ngot:\n%s\n%s", wj, gj, DiffText(string(wj), string(gj)))
}

// JSONString 把值序列化为紧凑 JSON；序列化失败时返回错误描述字符串（便于测试断言中直接打印）。
func JSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<json 序列化失败: " + err.Error() + ">"
	}
	return string(b)
}

// MustJSON 把值序列化为紧凑 JSON；失败时用 t.Fatalf 终止测试。
func MustJSON(t TestingT, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("xtest.MustJSON: 序列化失败: %v", err)
		return ""
	}
	return string(b)
}
