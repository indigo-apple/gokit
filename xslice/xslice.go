// Package xslice 提供切片算法：去重、变换、查找、分组、集合运算、排序、聚合、滑窗。
//
// 设计要点：
//   - **nil 安全**：传入 nil 切片时，返回切片的函数返回 nil，返回标量的函数返回零值，
//     所有函数都不 panic；
//   - **保序**：Dedup / Union / Intersect 等集合运算保持「首次出现」的顺序（稳定），
//     SortBy 系列一律使用**稳定排序**，避免同键元素顺序抖动；
//   - **不可变优先**：默认返回新切片；需要复用底层数组的函数以 InPlace 结尾，
//     调用前请确认不再使用原切片（可能与调用方共享底层数组）；
//   - **与 xmap 的分工**：键值对统一使用 xmap.Pair，Zip/Unzip 的返回值类型也在那边定义。
//
// 并发安全：本包全部为纯函数，可并发调用（InPlace 变体除外，需调用方自行同步）。
package xslice

import (
	"math/rand"
	"sort"

	"github.com/zhangwenxing/gokit/xconstraints"
	"github.com/zhangwenxing/gokit/xmap"
	"github.com/zhangwenxing/gokit/xreflect"
)

// ---------------------------------------------------------------------------
// 去重
// ---------------------------------------------------------------------------

// Dedup 去重并保持首次出现的顺序。
func Dedup[T comparable](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[T]struct{}, len(s))
	out := make([]T, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// DedupBy 按提取出的键去重，保留每个键首次出现的元素。
func DedupBy[T any, K comparable](s []T, key func(T) K) []T {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[K]struct{}, len(s))
	out := make([]T, 0, len(s))
	for _, v := range s {
		k := key(v)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return out
}

// DedupLast 去重，保留每个元素**最后一次**出现的位置，并保持这些位置的原始顺序。
//
//	[1 2 1 3] → [2 1 3]
func DedupLast[T comparable](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	lastIdx := make(map[T]int, len(s))
	for i, v := range s {
		lastIdx[v] = i
	}
	out := make([]T, 0, len(lastIdx))
	for i, v := range s {
		if lastIdx[v] == i {
			out = append(out, v)
		}
	}
	return out
}

// DedupByLast 按 key 去重，保留每个键最后一次出现的元素。
func DedupByLast[T any, K comparable](s []T, key func(T) K) []T {
	if len(s) == 0 {
		return nil
	}
	lastIdx := make(map[K]int, len(s))
	for i, v := range s {
		lastIdx[key(v)] = i
	}
	out := make([]T, 0, len(lastIdx))
	for i, v := range s {
		if lastIdx[key(v)] == i {
			out = append(out, v)
		}
	}
	return out
}

// DedupInPlace 原地去重（复用底层数组，零额外分配）。
//
// 返回值可能与入参共享底层数组，入参若仍被使用会出现内容不一致。
//
//	⚠️ 会修改入参切片的内容。
func DedupInPlace[T comparable](s []T) []T {
	if len(s) < 2 {
		return s
	}
	seen := make(map[T]struct{}, len(s))
	w := 0
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		s[w] = v
		w++
	}
	return s[:w]
}

// CountBy 统计每个键出现的次数。
func CountBy[T any, K comparable](s []T, key func(T) K) map[K]int {
	if len(s) == 0 {
		return nil
	}
	out := make(map[K]int, len(s))
	for _, v := range s {
		out[key(v)]++
	}
	return out
}

// Duplicates 返回出现次数大于 1 的元素（保序去重）。
func Duplicates[T comparable](s []T) []T {
	counts := make(map[T]int, len(s))
	for _, v := range s {
		counts[v]++
	}
	var out []T
	seen := make(map[T]struct{})
	for _, v := range s {
		if counts[v] > 1 {
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				out = append(out, v)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 变换
// ---------------------------------------------------------------------------

// Map 逐元素变换。
func Map[T, R any](s []T, fn func(T) R) []R {
	if len(s) == 0 {
		return nil
	}
	out := make([]R, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}

// MapIdx 带下标的变换。
func MapIdx[T, R any](s []T, fn func(int, T) R) []R {
	if len(s) == 0 {
		return nil
	}
	out := make([]R, len(s))
	for i, v := range s {
		out[i] = fn(i, v)
	}
	return out
}

// MapErr 变换，遇到错误立即返回（已完成的部分结果一并返回）。
func MapErr[T, R any](s []T, fn func(T) (R, error)) ([]R, error) {
	if len(s) == 0 {
		return nil, nil
	}
	out := make([]R, 0, len(s))
	for _, v := range s {
		r, err := fn(v)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// MapConcat 变换并拼接返回的切片（扁平映射）。
func MapConcat[T, R any](s []T, fn func(T) []R) []R {
	var out []R
	for _, v := range s {
		out = append(out, fn(v)...)
	}
	return out
}

// FilterMap 变换并过滤：fn 返回 false 时丢弃该元素。
func FilterMap[T, R any](s []T, fn func(T) (R, bool)) []R {
	if len(s) == 0 {
		return nil
	}
	out := make([]R, 0, len(s))
	for _, v := range s {
		if r, ok := fn(v); ok {
			out = append(out, r)
		}
	}
	return out
}

// Filter 保留满足条件的元素。
func Filter[T any](s []T, pred func(T) bool) []T {
	if len(s) == 0 {
		return nil
	}
	out := make([]T, 0, len(s))
	for _, v := range s {
		if pred(v) {
			out = append(out, v)
		}
	}
	return out
}

// FilterIdx 带下标的过滤。
func FilterIdx[T any](s []T, pred func(int, T) bool) []T {
	if len(s) == 0 {
		return nil
	}
	out := make([]T, 0, len(s))
	for i, v := range s {
		if pred(i, v) {
			out = append(out, v)
		}
	}
	return out
}

// Reject 丢弃满足条件的元素（Filter 取反）。
func Reject[T any](s []T, pred func(T) bool) []T {
	return Filter(s, func(v T) bool { return !pred(v) })
}

// FilterNil 剔除 nil 指针，返回解引用后的值。
func FilterNil[T any](s []*T) []T {
	if len(s) == 0 {
		return nil
	}
	out := make([]T, 0, len(s))
	for _, p := range s {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// Compact 去除零值元素。
func Compact[T comparable](s []T) []T {
	var zero T
	return Filter(s, func(v T) bool { return v != zero })
}

// Reduce 从左到右归约。
func Reduce[T, A any](s []T, init A, fn func(acc A, v T) A) A {
	acc := init
	for _, v := range s {
		acc = fn(acc, v)
	}
	return acc
}

// ReduceRight 从右到左归约。
func ReduceRight[T, A any](s []T, init A, fn func(acc A, v T) A) A {
	acc := init
	for i := len(s) - 1; i >= 0; i-- {
		acc = fn(acc, s[i])
	}
	return acc
}

// ForEach 遍历。
func ForEach[T any](s []T, fn func(T)) {
	if fn == nil {
		return
	}
	for _, v := range s {
		fn(v)
	}
}

// ForEachIdx 带下标遍历。
func ForEachIdx[T any](s []T, fn func(int, T)) {
	if fn == nil {
		return
	}
	for i, v := range s {
		fn(i, v)
	}
}

// ForEachErr 遍历，遇到错误立即停止。
func ForEachErr[T any](s []T, fn func(T) error) error {
	for _, v := range s {
		if fn == nil {
			return nil
		}
		if err := fn(v); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 查找
// ---------------------------------------------------------------------------

// Contains 判断是否包含某元素。
func Contains[T comparable](s []T, v T) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// ContainsBy 判断是否存在满足条件的元素。
func ContainsBy[T any](s []T, pred func(T) bool) bool {
	for _, item := range s {
		if pred(item) {
			return true
		}
	}
	return false
}

// IndexOf 返回元素首次出现的下标，未找到返回 -1。
func IndexOf[T comparable](s []T, v T) int {
	for i, item := range s {
		if item == v {
			return i
		}
	}
	return -1
}

// IndexOfBy 返回首个满足条件的下标，未找到返回 -1。
func IndexOfBy[T any](s []T, pred func(T) bool) int {
	for i, item := range s {
		if pred(item) {
			return i
		}
	}
	return -1
}

// LastIndexOf 返回元素最后一次出现的下标，未找到返回 -1。
func LastIndexOf[T comparable](s []T, v T) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// Find 返回首个满足条件的元素。
func Find[T any](s []T, pred func(T) bool) (T, bool) {
	for _, item := range s {
		if pred(item) {
			return item, true
		}
	}
	var zero T
	return zero, false
}

// FindIdx 返回首个满足条件的元素及其下标。
func FindIdx[T any](s []T, pred func(T) bool) (int, bool) {
	for i, item := range s {
		if pred(item) {
			return i, true
		}
	}
	return -1, false
}

// FindLast 返回最后一个满足条件的元素。
func FindLast[T any](s []T, pred func(T) bool) (T, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if pred(s[i]) {
			return s[i], true
		}
	}
	var zero T
	return zero, false
}

// Any 判断是否存在满足条件的元素（空切片返回 false）。
func Any[T any](s []T, pred func(T) bool) bool { return ContainsBy(s, pred) }

// All 判断是否全部满足条件（空切片返回 true，与数学约定一致）。
func All[T any](s []T, pred func(T) bool) bool {
	for _, item := range s {
		if !pred(item) {
			return false
		}
	}
	return true
}

// None 判断是否没有任何元素满足条件。
func None[T any](s []T, pred func(T) bool) bool { return !ContainsBy(s, pred) }

// Count 统计满足条件的元素个数。
func Count[T any](s []T, pred func(T) bool) int {
	n := 0
	for _, item := range s {
		if pred(item) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 分组与切分
// ---------------------------------------------------------------------------

// GroupBy 按 key 分组。
func GroupBy[T any, K comparable](s []T, key func(T) K) map[K][]T {
	if len(s) == 0 {
		return nil
	}
	out := make(map[K][]T)
	for _, v := range s {
		k := key(v)
		out[k] = append(out[k], v)
	}
	return out
}

// GroupByOrdered 按 key 分组，并返回键的首次出现顺序。
func GroupByOrdered[T any, K comparable](s []T, key func(T) K) ([]K, map[K][]T) {
	if len(s) == 0 {
		return nil, nil
	}
	order := make([]K, 0, len(s))
	out := make(map[K][]T)
	for _, v := range s {
		k := key(v)
		if _, exists := out[k]; !exists {
			order = append(order, k)
		}
		out[k] = append(out[k], v)
	}
	return order, out
}

// Partition 一分为二：满足条件的与不满足条件的。
func Partition[T any](s []T, pred func(T) bool) (yes, no []T) {
	for _, v := range s {
		if pred(v) {
			yes = append(yes, v)
		} else {
			no = append(no, v)
		}
	}
	return yes, no
}

// Chunk 按固定大小切分成多块，最后一块可能不足 size。
func Chunk[T any](s []T, size int) [][]T {
	if len(s) == 0 || size <= 0 {
		return nil
	}
	out := make([][]T, 0, (len(s)+size-1)/size)
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// Window 返回滑动窗口；step 省略时默认为 1。
//
//	Window([1 2 3 4], 3)     → [[1 2 3] [2 3 4]]
//	Window([1 2 3 4], 2, 2)  → [[1 2] [3 4]]
func Window[T any](s []T, size int, step ...int) [][]T {
	if size <= 0 || len(s) < size {
		return nil
	}
	st := 1
	if len(step) > 0 && step[0] > 0 {
		st = step[0]
	}
	out := make([][]T, 0, (len(s)-size)/st+1)
	for i := 0; i+size <= len(s); i += st {
		out = append(out, s[i:i+size])
	}
	return out
}

// SplitAt 在下标 i 处切分（i 会被夹到 [0, len(s)]）。
func SplitAt[T any](s []T, i int) (left, right []T) {
	if i < 0 {
		i = 0
	}
	if i > len(s) {
		i = len(s)
	}
	return s[:i], s[i:]
}

// SplitBy 按谓词分段：谓词为 true 的元素作为分隔点（分隔点本身被丢弃）。
func SplitBy[T any](s []T, sep func(T) bool) [][]T {
	var out [][]T
	var cur []T
	for _, v := range s {
		if sep(v) {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, v)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// Flatten 把二维切片展平为一维。
func Flatten[T any](s [][]T) []T {
	total := 0
	for _, sub := range s {
		total += len(sub)
	}
	if total == 0 {
		return nil
	}
	out := make([]T, 0, total)
	for _, sub := range s {
		out = append(out, sub...)
	}
	return out
}

// ---------------------------------------------------------------------------
// 集合运算
// ---------------------------------------------------------------------------

// Union 求并集（保序去重）。
func Union[T comparable](lists ...[]T) []T {
	var out []T
	seen := make(map[T]struct{})
	for _, l := range lists {
		for _, v := range l {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// Intersect 求交集（按第一个切片的顺序，去重）。
func Intersect[T comparable](lists ...[]T) []T {
	if len(lists) == 0 || len(lists[0]) == 0 {
		return nil
	}
	if len(lists) == 1 {
		return Dedup(lists[0])
	}
	// 计数每个元素出现在多少个列表中（同一切片内的重复只算一次）
	counts := make(map[T]int)
	for _, l := range lists {
		seen := make(map[T]struct{}, len(l))
		for _, v := range l {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			counts[v]++
		}
	}
	out := make([]T, 0)
	added := make(map[T]struct{})
	for _, v := range lists[0] {
		if counts[v] == len(lists) {
			if _, ok := added[v]; ok {
				continue
			}
			added[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// Difference 返回「在 a 中但不在 b 中」的元素（保序去重）。
func Difference[T comparable](a, b []T) []T {
	if len(a) == 0 {
		return nil
	}
	exclude := make(map[T]struct{}, len(b))
	for _, v := range b {
		exclude[v] = struct{}{}
	}
	out := make([]T, 0, len(a))
	seen := make(map[T]struct{})
	for _, v := range a {
		if _, skip := exclude[v]; skip {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// SymmetricDifference 返回只出现在其中一个切片里的元素。
func SymmetricDifference[T comparable](a, b []T) []T {
	return Union(Difference(a, b), Difference(b, a))
}

// IsSubset 判断 a 是否为 b 的子集（含空集）。
func IsSubset[T comparable](a, b []T) bool {
	set := make(map[T]struct{}, len(b))
	for _, v := range b {
		set[v] = struct{}{}
	}
	for _, v := range a {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}

// Overlaps 判断两个切片是否有交集。
func Overlaps[T comparable](a, b []T) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[T]struct{}, len(a))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := set[v]; ok {
			return true
		}
	}
	return false
}

// Equal 判断两个切片是否**顺序与内容都相同**。
func Equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// EqualUnordered 判断两个切片**内容相同但顺序无关**（自动处理重复元素）。
func EqualUnordered[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[T]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 顺序操作
// ---------------------------------------------------------------------------

// Reverse 返回反转后的新切片。
func Reverse[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	out := make([]T, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// ReverseInPlace 原地反转。⚠️ 会修改入参。
func ReverseInPlace[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Shuffle 就地随机打乱（使用全局 math/rand，非安全用途）。⚠️ 会修改入参。
func Shuffle[T any](s []T) {
	rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
}

// ShuffleCopy 返回打乱后的新切片，不修改入参。
func ShuffleCopy[T any](s []T) []T { return ShuffleCopyN(s, rand.Intn) }

// ShuffleCopyN 使用自定义随机源打乱（便于测试可复现）。
func ShuffleCopyN[T any](s []T, intn func(n int) int) []T {
	out := Clone(s)
	if intn == nil {
		return out
	}
	for i := len(out) - 1; i > 0; i-- {
		j := intn(i + 1)
		if j < 0 || j > i {
			continue
		}
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Rotate 循环左移 n 位（n 为负则右移）。
//
//	Rotate([1 2 3 4 5], 2)  → [3 4 5 1 2]
func Rotate[T any](s []T, n int) []T {
	if len(s) == 0 {
		return nil
	}
	k := ((n % len(s)) + len(s)) % len(s)
	if k == 0 {
		return Clone(s)
	}
	out := make([]T, 0, len(s))
	out = append(out, s[k:]...)
	out = append(out, s[:k]...)
	return out
}

// Take 返回前 n 个元素；n 超出长度时返回全部。
func Take[T any](s []T, n int) []T {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	if n >= len(s) {
		return Clone(s)
	}
	return Clone(s[:n])
}

// TakeLast 返回最后 n 个元素。
func TakeLast[T any](s []T, n int) []T {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	if n >= len(s) {
		return Clone(s)
	}
	return Clone(s[len(s)-n:])
}

// TakeWhile 返回从头开始连续满足条件的元素。
func TakeWhile[T any](s []T, pred func(T) bool) []T {
	for i, v := range s {
		if !pred(v) {
			return Clone(s[:i])
		}
	}
	return Clone(s)
}

// DropWhile 丢弃从头开始连续满足条件的元素，返回剩余部分。
func DropWhile[T any](s []T, pred func(T) bool) []T {
	for i, v := range s {
		if !pred(v) {
			return Clone(s[i:])
		}
	}
	return nil
}

// Skip 跳过前 n 个元素。
func Skip[T any](s []T, n int) []T {
	if n <= 0 {
		return Clone(s)
	}
	if n >= len(s) {
		return nil
	}
	return Clone(s[n:])
}

// SkipWhile 是 DropWhile 的别名。
func SkipWhile[T any](s []T, pred func(T) bool) []T { return DropWhile(s, pred) }

// Head 返回第一个元素。
func Head[T any](s []T) (T, bool) {
	if len(s) == 0 {
		var zero T
		return zero, false
	}
	return s[0], true
}

// Last 返回最后一个元素。
func Last[T any](s []T) (T, bool) {
	if len(s) == 0 {
		var zero T
		return zero, false
	}
	return s[len(s)-1], true
}

// Tail 返回除第一个元素外的全部元素。
func Tail[T any](s []T) []T {
	if len(s) <= 1 {
		return nil
	}
	return Clone(s[1:])
}

// ---------------------------------------------------------------------------
// 增删改
// ---------------------------------------------------------------------------

// Insert 在下标 i 处插入若干元素（i 会被夹到 [0, len(s)]）。
func Insert[T any](s []T, i int, vs ...T) []T {
	if i < 0 {
		i = 0
	}
	if i > len(s) {
		i = len(s)
	}
	if len(vs) == 0 {
		return Clone(s)
	}
	out := make([]T, 0, len(s)+len(vs))
	out = append(out, s[:i]...)
	out = append(out, vs...)
	out = append(out, s[i:]...)
	return out
}

// RemoveAt 删除下标 i 处的元素；越界时返回原切片副本。
func RemoveAt[T any](s []T, i int) []T {
	if i < 0 || i >= len(s) {
		return Clone(s)
	}
	out := make([]T, 0, len(s)-1)
	out = append(out, s[:i]...)
	out = append(out, s[i+1:]...)
	return out
}

// Remove 删除首个等于 v 的元素。
func Remove[T comparable](s []T, v T) []T {
	i := IndexOf(s, v)
	if i < 0 {
		return Clone(s)
	}
	return RemoveAt(s, i)
}

// RemoveAll 删除全部出现在 vs 中的元素。
func RemoveAll[T comparable](s []T, vs ...T) []T {
	if len(vs) == 0 || len(s) == 0 {
		return Clone(s)
	}
	drop := make(map[T]struct{}, len(vs))
	for _, v := range vs {
		drop[v] = struct{}{}
	}
	out := make([]T, 0, len(s))
	for _, v := range s {
		if _, ok := drop[v]; !ok {
			out = append(out, v)
		}
	}
	return out
}

// RemoveBy 删除全部满足条件的元素。
func RemoveBy[T any](s []T, pred func(T) bool) []T {
	return Reject(s, pred)
}

// Replace 把 old 替换为 new（n <= 0 表示全部替换）。
func Replace[T comparable](s []T, old, new T, n ...int) []T {
	limit := -1
	if len(n) > 0 {
		limit = n[0]
	}
	out := Clone(s)
	done := 0
	for i, v := range out {
		if v != old {
			continue
		}
		if limit > 0 && done >= limit {
			break
		}
		out[i] = new
		done++
	}
	return out
}

// Concat 拼接多个切片。
func Concat[T any](lists ...[]T) []T {
	total := 0
	for _, l := range lists {
		total += len(l)
	}
	if total == 0 {
		return nil
	}
	out := make([]T, 0, total)
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// Repeat 重复元素 n 次生成新切片。
func Repeat[T any](v T, n int) []T {
	if n <= 0 {
		return nil
	}
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// Pad 把切片补齐到长度 n，多出的部分用 fill 填充。
func Pad[T any](s []T, n int, fill T) []T {
	if n <= len(s) {
		return Clone(s)
	}
	out := make([]T, n)
	copy(out, s)
	for i := len(s); i < n; i++ {
		out[i] = fill
	}
	return out
}

// Clone 返回切片的浅拷贝。
//
// 非 nil 的空切片会返回非 nil 的副本（与标准库 slices.Clone 一致），
// nil 切片返回 nil。
func Clone[T any](s []T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// CloneDeep 返回切片的深拷贝（保留别名与循环引用）。
func CloneDeep[T any](s []T) []T { return xreflect.DeepCopy(s) }

// ---------------------------------------------------------------------------
// 排序
// ---------------------------------------------------------------------------

// SortBy 按 key 排序（稳定），desc 为 true 时降序。
func SortBy[T any, K xconstraints.Ordered](s []T, key func(T) K, desc ...bool) []T {
	out := Clone(s)
	if len(out) < 2 {
		return out
	}
	d := len(desc) > 0 && desc[0]
	sort.SliceStable(out, func(i, j int) bool {
		a, b := key(out[i]), key(out[j])
		if d {
			return a > b
		}
		return a < b
	})
	return out
}

// SortStableBy 是 SortBy 的显式别名，强调稳定排序语义。
func SortStableBy[T any, K xconstraints.Ordered](s []T, key func(T) K, desc ...bool) []T {
	return SortBy(s, key, desc...)
}

// SortMulti 按多个比较器依次比较（稳定）。
//
// 每个 less 返回 -1 / 0 / 1，为 0 时继续用下一个比较器。
func SortMulti[T any](s []T, less ...func(a, b T) int) []T {
	out := Clone(s)
	if len(out) < 2 || len(less) == 0 {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		for _, l := range less {
			if l == nil {
				continue
			}
			if c := l(out[i], out[j]); c != 0 {
				return c < 0
			}
		}
		return false
	})
	return out
}

// SortOrdered 对有序类型切片升序排序（返回新切片）。
func SortOrdered[T xconstraints.Ordered](s []T) []T {
	out := Clone(s)
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SortOrderedInPlace 就地升序排序。⚠️ 会修改入参。
func SortOrderedInPlace[T xconstraints.Ordered](s []T) {
	sort.SliceStable(s, func(i, j int) bool { return s[i] < s[j] })
}

// MinBy 返回 key 最小的元素。
func MinBy[T any, K xconstraints.Ordered](s []T, key func(T) K) (T, bool) {
	var best T
	found := false
	var bestKey K
	for _, v := range s {
		k := key(v)
		if !found || k < bestKey {
			best, bestKey, found = v, k, true
		}
	}
	return best, found
}

// MaxBy 返回 key 最大的元素。
func MaxBy[T any, K xconstraints.Ordered](s []T, key func(T) K) (T, bool) {
	var best T
	found := false
	var bestKey K
	for _, v := range s {
		k := key(v)
		if !found || k > bestKey {
			best, bestKey, found = v, k, true
		}
	}
	return best, found
}

// ---------------------------------------------------------------------------
// 聚合
// ---------------------------------------------------------------------------

// Sum 求和。
func Sum[T xconstraints.Number](s []T) T {
	var r T
	for _, v := range s {
		r += v
	}
	return r
}

// Average 求平均；空切片返回 0。
func Average[T xconstraints.Number](s []T) float64 {
	if len(s) == 0 {
		return 0
	}
	var r float64
	for _, v := range s {
		r += float64(v)
	}
	return r / float64(len(s))
}

// Min 返回最小值。
func Min[T xconstraints.Ordered](s []T) (T, bool) {
	var best T
	if len(s) == 0 {
		return best, false
	}
	best = s[0]
	for _, v := range s[1:] {
		if v < best {
			best = v
		}
	}
	return best, true
}

// Max 返回最大值。
func Max[T xconstraints.Ordered](s []T) (T, bool) {
	var best T
	if len(s) == 0 {
		return best, false
	}
	best = s[0]
	for _, v := range s[1:] {
		if v > best {
			best = v
		}
	}
	return best, true
}

// MinMax 同时返回最小值与最大值。
func MinMax[T xconstraints.Ordered](s []T) (min, max T, ok bool) {
	if len(s) == 0 {
		return min, max, false
	}
	min, max = s[0], s[0]
	for _, v := range s[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max, true
}

// ---------------------------------------------------------------------------
// 键值对
// ---------------------------------------------------------------------------

// Zip 把两个切片按短的一方配对。
func Zip[A, B any](a []A, b []B) []xmap.Pair[A, B] {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return nil
	}
	out := make([]xmap.Pair[A, B], n)
	for i := 0; i < n; i++ {
		out[i] = xmap.Pair[A, B]{Key: a[i], Val: b[i]}
	}
	return out
}

// Unzip 把键值对拆回两个切片。
func Unzip[A, B any](pairs []xmap.Pair[A, B]) ([]A, []B) {
	if len(pairs) == 0 {
		return nil, nil
	}
	as := make([]A, len(pairs))
	bs := make([]B, len(pairs))
	for i, p := range pairs {
		as[i], bs[i] = p.Key, p.Val
	}
	return as, bs
}

// ToMap 把切片转为 map。
func ToMap[T any, K comparable, V any](s []T, keyFn func(T) K, valFn func(T) V) map[K]V {
	return xmap.FromSlice(s, keyFn, valFn)
}

// ToSet 把切片转为集合。
func ToSet[T comparable](s []T) map[T]struct{} {
	if len(s) == 0 {
		return nil
	}
	out := make(map[T]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

// ---------------------------------------------------------------------------
// 随机
// ---------------------------------------------------------------------------

// Rand 随机返回一个元素。
func Rand[T any](s []T) (T, bool) {
	if len(s) == 0 {
		var zero T
		return zero, false
	}
	return s[rand.Intn(len(s))], true
}

// Sample 随机抽取 n 个不重复元素；n 超出长度时返回全部（打乱顺序）。
func Sample[T any](s []T, n int) []T {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	out := ShuffleCopy(s)
	if n >= len(out) {
		return out
	}
	return out[:n]
}

// SampleN 使用自定义随机源抽取（便于测试可复现）。
func SampleN[T any](s []T, n int, intn func(int) int) []T {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	out := ShuffleCopyN(s, intn)
	if n >= len(out) {
		return out
	}
	return out[:n]
}
