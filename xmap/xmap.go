// Package xmap 提供 map 相关算法：键值遍历、筛选、映射、合并、集合运算、嵌套路径读写。
//
// 设计要点：
//   - **遍历顺序问题**：Go 的 map 遍历顺序是随机的，
//     需要稳定输出（日志、报表、测试、签名）时请使用 ForEachSorted / KeysSorted / EntriesSorted；
//   - **nil 语义统一**：传入 nil map 时，返回集合的函数返回 nil（与标准库习惯一致），
//     返回标量的函数返回零值，绝不 panic；
//   - **返回新 map**：所有变换函数都返回新 map，不修改入参；
//     需要写入已有 map 请使用 Copy；
//   - 嵌套路径读写（GetPath / SetPath）仅支持 `map[string]any` 与 `[]any` 组合，
//     这是 JSON 解码后的自然形态。
//
// 并发安全：本包全部为纯函数，可并发调用（调用方需保证入参 map 本身不被并发改写）。
package xmap

import (
	"strconv"
	"strings"

	"github.com/zhangwenxing/gokit/xconstraints"
	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xreflect"
)

// Pair 表示一个键值对，是全工具链统一的「键值对」类型
// （xslice.Zip、xmap.Entries 等都以它作为交换格式）。
//
// Key 刻意不约束为 comparable：Pair 只作数据载体使用，
// 若加上约束会让 xslice.Zip 无法处理切片 / map 这类不可比较的元素。
// 需要由 Pair 构建 map 时，请使用 xmap.FromEntries。
//
// 注意：这里不提供 `Entry` 别名，因为泛型类型别名需要 Go 1.24+，
// 而本工具链基线是 Go 1.21。
type Pair[K any, V any] struct {
	Key K
	Val V
}

// ---------------------------------------------------------------------------
// 基础查询
// ---------------------------------------------------------------------------

// Len 返回 map 长度（nil 安全）。
func Len[K comparable, V any](m map[K]V) int { return len(m) }

// IsEmpty 判断 map 是否为空或 nil。
func IsEmpty[K comparable, V any](m map[K]V) bool { return len(m) == 0 }

// Keys 返回全部键（顺序随机）。
func Keys[K comparable, V any](m map[K]V) []K {
	if m == nil {
		return nil
	}
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// KeysSorted 返回按升序排列的键。
func KeysSorted[K xconstraints.Ordered, V any](m map[K]V) []K {
	out := Keys(m)
	SortOrdered(out)
	return out
}

// SortOrdered 就地升序排序（稳定）。
//
// 内部使用归并排序而非快排：既保证稳定（同值元素相对顺序不变），
// 也不存在「分区无进展导致死循环」的边界风险。
func SortOrdered[T xconstraints.Ordered](s []T) {
	stableSortBy(s, func(a, b T) bool { return a < b })
}

// KeysSortedBy 按键的降序 / 自定义比较器排序。
//
// desc 为 true 时降序。
func KeysSortedBy[K xconstraints.Ordered, V any](m map[K]V, desc bool) []K {
	out := KeysSorted(m)
	if desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// Values 返回全部值（顺序随机）。
func Values[K comparable, V any](m map[K]V) []V {
	if m == nil {
		return nil
	}
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// ValuesSortedByKey 按 key 升序返回值。
func ValuesSortedByKey[K xconstraints.Ordered, V any](m map[K]V) []V {
	if m == nil {
		return nil
	}
	out := make([]V, 0, len(m))
	for _, k := range KeysSorted(m) {
		out = append(out, m[k])
	}
	return out
}

// Entries 返回全部键值对（顺序随机）。
func Entries[K comparable, V any](m map[K]V) []Pair[K, V] {
	if m == nil {
		return nil
	}
	out := make([]Pair[K, V], 0, len(m))
	for k, v := range m {
		out = append(out, Pair[K, V]{Key: k, Val: v})
	}
	return out
}

// EntriesSorted 返回按 key 升序排列的键值对。
func EntriesSorted[K xconstraints.Ordered, V any](m map[K]V) []Pair[K, V] {
	if m == nil {
		return nil
	}
	out := make([]Pair[K, V], 0, len(m))
	for _, k := range KeysSorted(m) {
		out = append(out, Pair[K, V]{Key: k, Val: m[k]})
	}
	return out
}

// Has 判断键是否存在。
func Has[K comparable, V any](m map[K]V, k K) bool {
	_, ok := m[k]
	return ok
}

// Get 返回键对应的值与是否存在。
func Get[K comparable, V any](m map[K]V, k K) (V, bool) {
	v, ok := m[k]
	return v, ok
}

// GetOr 返回键对应的值，不存在时返回默认值。
func GetOr[K comparable, V any](m map[K]V, k K, def V) V {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

// GetOrSet 键不存在时写入默认值并返回。
//
// 注意：m 必须是非 nil 的 map，否则会 panic（与 Go 语言语义一致）。
func GetOrSet[K comparable, V any](m map[K]V, k K, def V) V {
	if v, ok := m[k]; ok {
		return v
	}
	m[k] = def
	return def
}

// Delete 删除键并返回被删除的值。
func Delete[K comparable, V any](m map[K]V, k K) (V, bool) {
	v, ok := m[k]
	if ok {
		delete(m, k)
	}
	return v, ok
}

// SetDefault 仅当键不存在时写入。
func SetDefault[K comparable, V any](m map[K]V, k K, def V) {
	if _, ok := m[k]; !ok {
		m[k] = def
	}
}

// ---------------------------------------------------------------------------
// 变换
// ---------------------------------------------------------------------------

// MapValues 对每个值做变换。
func MapValues[K comparable, V, R any](m map[K]V, fn func(V) R) map[K]R {
	if m == nil {
		return nil
	}
	out := make(map[K]R, len(m))
	for k, v := range m {
		out[k] = fn(v)
	}
	return out
}

// MapValuesKV 对每个键值做变换（需要访问键时使用）。
func MapValuesKV[K comparable, V, R any](m map[K]V, fn func(K, V) R) map[K]R {
	if m == nil {
		return nil
	}
	out := make(map[K]R, len(m))
	for k, v := range m {
		out[k] = fn(k, v)
	}
	return out
}

// MapKeys 对每个键做变换（可能产生键冲突，后者覆盖前者）。
func MapKeys[K, K2 comparable, V any](m map[K]V, fn func(K, V) K2) map[K2]V {
	if m == nil {
		return nil
	}
	out := make(map[K2]V, len(m))
	for k, v := range m {
		out[fn(k, v)] = v
	}
	return out
}

// Filter 按谓词筛选键值对。
func Filter[K comparable, V any](m map[K]V, pred func(K, V) bool) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V)
	for k, v := range m {
		if pred(k, v) {
			out[k] = v
		}
	}
	return out
}

// FilterKeys 按键筛选。
func FilterKeys[K comparable, V any](m map[K]V, pred func(K) bool) map[K]V {
	return Filter(m, func(k K, _ V) bool { return pred(k) })
}

// FilterValues 按值筛选。
func FilterValues[K comparable, V any](m map[K]V, pred func(V) bool) map[K]V {
	return Filter(m, func(_ K, v V) bool { return pred(v) })
}

// Invert 交换键值；值必须可比较。值重复时后者覆盖前者。
func Invert[K, V comparable](m map[K]V) map[V]K {
	if m == nil {
		return nil
	}
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// Pick 只保留指定键。
func Pick[K comparable, V any](m map[K]V, keys ...K) map[K]V {
	out := make(map[K]V, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Omit 排除指定键。
func Omit[K comparable, V any](m map[K]V, keys ...K) map[K]V {
	if m == nil {
		return nil
	}
	skip := make(map[K]struct{}, len(keys))
	for _, k := range keys {
		skip[k] = struct{}{}
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		if _, drop := skip[k]; !drop {
			out[k] = v
		}
	}
	return out
}

// GroupByValue 按值的特征把键值对分组。
func GroupByValue[K comparable, V any, G comparable](m map[K]V, key func(K, V) G) map[G]map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[G]map[K]V)
	for k, v := range m {
		g := key(k, v)
		if out[g] == nil {
			out[g] = make(map[K]V)
		}
		out[g][k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// 合并与集合运算
// ---------------------------------------------------------------------------

// Merge 合并多个 map，**后者覆盖前者**。
func Merge[K comparable, V any](maps ...map[K]V) map[K]V {
	total := 0
	for _, m := range maps {
		total += len(m)
	}
	out := make(map[K]V, total)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// MergeKeepFirst 合并多个 map，**前者优先**。
func MergeKeepFirst[K comparable, V any](maps ...map[K]V) map[K]V {
	total := 0
	for _, m := range maps {
		total += len(m)
	}
	out := make(map[K]V, total)
	for _, m := range maps {
		for k, v := range m {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}

// MergeWith 用一个解决函数处理键冲突。
//
//	resolve(old, new) 返回最终值。
func MergeWith[K comparable, V any](resolve func(old, new V) V, maps ...map[K]V) map[K]V {
	total := 0
	for _, m := range maps {
		total += len(m)
	}
	out := make(map[K]V, total)
	for _, m := range maps {
		for k, v := range m {
			if old, exists := out[k]; exists && resolve != nil {
				out[k] = resolve(old, v)
			} else {
				out[k] = v
			}
		}
	}
	return out
}

// MergeDeep 递归合并：当同名键的值**都是 map[string]any** 时递归合并，否则后者覆盖前者。
func MergeDeep(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if oldMap, ok := out[k].(map[string]any); ok {
			if newMap, ok2 := v.(map[string]any); ok2 {
				out[k] = MergeDeep(oldMap, newMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// Diff 返回 a 中「不存在于 b 或值不同」的键值对。
func Diff[K comparable, V comparable](a, b map[K]V) map[K]V {
	out := make(map[K]V)
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			out[k] = v
		}
	}
	return out
}

// DiffKeys 返回仅在 a / 仅在 b / 两者都有的键。
func DiffKeys[K comparable, V any](a, b map[K]V) (onlyA, onlyB, both []K) {
	for k := range a {
		if _, ok := b[k]; ok {
			both = append(both, k)
		} else {
			onlyA = append(onlyA, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			onlyB = append(onlyB, k)
		}
	}
	return
}

// IntersectKeys 返回两个 map 都存在的键值对（值取 a 的）。
func IntersectKeys[K comparable, V any](a, b map[K]V) map[K]V {
	out := make(map[K]V)
	for k, v := range a {
		if _, ok := b[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Equal 判断两个 map 是否完全一致（键与值都相同）。
func Equal[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 遍历
// ---------------------------------------------------------------------------

// ForEach 遍历（顺序随机）。
func ForEach[K comparable, V any](m map[K]V, fn func(K, V)) {
	if fn == nil {
		return
	}
	for k, v := range m {
		fn(k, v)
	}
}

// ForEachSorted 按键升序遍历。需要稳定输出时**必须**使用本函数。
func ForEachSorted[K xconstraints.Ordered, V any](m map[K]V, fn func(K, V)) {
	if fn == nil {
		return
	}
	for _, k := range KeysSorted(m) {
		fn(k, m[k])
	}
}

// ForEachSortedBy 按键的自定义顺序遍历。
func ForEachSortedBy[K comparable, V any](m map[K]V, less func(a, b K) bool, fn func(K, V)) {
	if fn == nil || len(m) == 0 {
		return
	}
	keys := Keys(m)
	if less != nil {
		stableSortBy(keys, less)
	}
	for _, k := range keys {
		fn(k, m[k])
	}
}

// stableSortBy 是仅依赖 less 的归并排序（稳定）。
func stableSortBy[T any](s []T, less func(a, b T) bool) {
	if len(s) < 2 {
		return
	}
	tmp := make([]T, len(s))
	mergeSort(s, tmp, less)
}

func mergeSort[T any](s, tmp []T, less func(a, b T) bool) {
	n := len(s)
	if n < 2 {
		return
	}
	mid := n / 2
	mergeSort(s[:mid], tmp[:mid], less)
	mergeSort(s[mid:], tmp[mid:], less)
	i, j, k := 0, mid, 0
	for i < mid && j < n {
		if less(s[j], s[i]) {
			tmp[k] = s[j]
			j++
		} else {
			tmp[k] = s[i]
			i++
		}
		k++
	}
	for i < mid {
		tmp[k] = s[i]
		i++
		k++
	}
	for j < n {
		tmp[k] = s[j]
		j++
		k++
	}
	copy(s, tmp)
}

// ---------------------------------------------------------------------------
// 嵌套路径
// ---------------------------------------------------------------------------

// splitPath 把 "a.b[0].c" 解析为 ["a","b","0","c"]。
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.FieldsFunc(path, func(r rune) bool {
		return r == '.' || r == '[' || r == ']'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetPath 按路径取值，支持 `map[string]any` 与 `[]any` 交替：
//
//	m := {"user": {"tags": []any{"a","b"}}}
//	GetPath(m, "user.tags[1]")   // "b", true
//	GetPath(m, "user.tags.0")    // 等价写法
func GetPath(m map[string]any, path string) (any, bool) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, false
	}
	var cur any = m
	for _, p := range parts {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// GetPathOr 按路径取值，不存在时返回默认值。
func GetPathOr[V any](m map[string]any, path string, def V) V {
	v, ok := GetPath(m, path)
	if !ok {
		return def
	}
	if typed, ok := v.(V); ok {
		return typed
	}
	// 尝试弱类型转换（例如 JSON 数字 float64 → int）
	var out V
	if err := xreflect.SetValue(&out, v); err == nil {
		return out
	}
	return def
}

// SetPath 按路径写入值，中间缺失的层级会自动创建为 map[string]any。
//
// 中间层级已存在但不是 map 时返回错误。
func SetPath(m map[string]any, path string, val any) error {
	if m == nil {
		return xerr.NewC(xerr.CodeInvalidParam, "xmap: SetPath on nil map")
	}
	parts := splitPath(path)
	if len(parts) == 0 {
		return xerr.NewfC(xerr.CodeInvalidParam, "xmap: empty path")
	}
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = val
			return nil
		}
		next, exists := cur[p]
		if !exists || next == nil {
			child := make(map[string]any)
			cur[p] = child
			cur = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return xerr.NewfC(xerr.CodeConflict,
				"xmap: path segment %q is %s, not a map", strings.Join(parts[:i+1], "."), xreflect.TypeName(next))
		}
		cur = child
	}
	return nil
}

// DeletePath 按路径删除键，并返回被删除的值。
func DeletePath(m map[string]any, path string) (any, bool) {
	parts := splitPath(path)
	if len(parts) == 0 || m == nil {
		return nil, false
	}
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			v, ok := cur[p]
			if ok {
				delete(cur, p)
			}
			return v, ok
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

// FlattenKeys 把嵌套 map 展平为 "a.b.c" 形式的键。
//
//	{"a": {"b": 1}}  →  {"a.b": 1}
func FlattenKeys(m map[string]any, sep string) map[string]any {
	if sep == "" {
		sep = "."
	}
	out := make(map[string]any)
	flattenInto(out, "", m, sep)
	return out
}

func flattenInto(out map[string]any, prefix string, m map[string]any, sep string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + sep + k
		}
		if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
			flattenInto(out, key, sub, sep)
			continue
		}
		out[key] = v
	}
}

// UnflattenKeys 是 FlattenKeys 的逆操作。
//
//	{"a.b": 1}  →  {"a": {"b": 1}}
//
// 当既是叶子又是中间节点时（如同时存在 "a" 与 "a.b"），中间节点优先保留叶子值，
// 冲突的路径会被写入到非 map 位置并返回错误。
func UnflattenKeys(m map[string]any, sep string) (map[string]any, error) {
	if sep == "" {
		sep = "."
	}
	out := make(map[string]any)
	// 为了保证确定性，按 key 排序处理
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	SortOrdered(keys)

	var errs xerr.MultiError
	for _, k := range keys {
		v := m[k]
		parts := strings.Split(k, sep)
		cur := out
		for i, p := range parts {
			if p == "" {
				continue
			}
			if i == len(parts)-1 {
				if existing, ok := cur[p]; ok {
					if _, isMap := existing.(map[string]any); isMap {
						errs = append(errs, xerr.NewfC(xerr.CodeConflict,
							"xmap: key %q conflicts with an existing nested map", k))
						continue
					}
				}
				cur[p] = v
				continue
			}
			next, ok := cur[p]
			if !ok {
				child := make(map[string]any)
				cur[p] = child
				cur = child
				continue
			}
			child, isMap := next.(map[string]any)
			if !isMap {
				errs = append(errs, xerr.NewfC(xerr.CodeConflict,
					"xmap: key %q conflicts with existing value at %q", k, p))
				break
			}
			cur = child
		}
	}
	if len(errs) > 0 {
		return out, errs
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 克隆与构建
// ---------------------------------------------------------------------------

// Clone 浅拷贝 map。
func Clone[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CloneDeep 深拷贝 map（使用 xreflect.DeepCopy，保留别名与循环引用）。
func CloneDeep[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	return xreflect.DeepCopy(m)
}

// Copy 把 src 的键值对写入 dst（dst 为 nil 时自动创建并返回）。
func Copy[K comparable, V any](dst, src map[K]V) map[K]V {
	if dst == nil {
		dst = make(map[K]V, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// FromSlice 由切片构建 map。
//
//	FromSlice([]User{{1,"a"},{2,"b"}}, func(u User) int64 { return u.ID })
func FromSlice[T any, K comparable, V any](s []T, keyFn func(T) K, valFn func(T) V) map[K]V {
	if len(s) == 0 {
		return nil
	}
	out := make(map[K]V, len(s))
	for _, item := range s {
		out[keyFn(item)] = valFn(item)
	}
	return out
}

// FromSliceSelf 由切片构建「键 → 元素本身」的 map。
func FromSliceSelf[T any, K comparable](s []T, keyFn func(T) K) map[K]T {
	if len(s) == 0 {
		return nil
	}
	out := make(map[K]T, len(s))
	for _, item := range s {
		out[keyFn(item)] = item
	}
	return out
}

// FromEntries 由键值对切片构建 map。
func FromEntries[K comparable, V any](pairs []Pair[K, V]) map[K]V {
	if len(pairs) == 0 {
		return nil
	}
	out := make(map[K]V, len(pairs))
	for _, p := range pairs {
		out[p.Key] = p.Val
	}
	return out
}
