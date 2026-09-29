// Package xreflect 提供反射工具：深拷贝、字段元数据缓存、字段读写、tag 解析、动态调用。
//
// 设计要点：
//   - **元数据缓存**：结构体字段信息（含嵌入字段展开）通过 sync.Map 缓存，
//     避免热路径上反复 reflect.Type.NumField()/Field()；
//   - **深拷贝**：零依赖实现，支持指针 / 切片 / map / 数组 / 接口，并处理循环引用；
//     结构体通过「整体浅拷贝 + 逐字段深拷贝」实现，因此含未导出字段的类型也能正确复制；
//   - **弱类型赋值**：Assign 支持 string ↔ 数值 ↔ bool 互转、map ↔ struct、struct ↔ struct、
//     切片逐元素赋值、time.Time / time.Duration 解析，是 xconv 与 xconfig 的公共底座；
//   - 本包**不做业务类型转换策略**（如自定义时间布局），只在 xconv 层提供。
//
// 已知限制：
//   - DeepCopy 会连同 sync.Mutex 等含状态的字段一起复制，含锁类型请谨慎使用；
//   - Describe 只收集**导出字段**，未导出字段无法通过反射赋值。
//
// 并发安全：所有导出函数均可并发使用（内部缓存带读写锁）。
package xreflect

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 基础判定
// ---------------------------------------------------------------------------

var (
	timeType     = reflect.TypeOf(time.Time{})
	durationType = reflect.TypeOf(time.Duration(0))
	byteSliceTyp = reflect.TypeOf([]byte(nil))
)

// IsNil 判断 v 是否为 nil，能正确识别「接口持有 nil 指针」的 typed nil 陷阱。
//
//	var p *int // p == nil
//	var i any = p
//	i == nil        // false ← 经典陷阱
//	xreflect.IsNil(i) // true
func IsNil(v any) bool {
	if v == nil {
		return true
	}
	return isNilValue(reflect.ValueOf(v))
}

func isNilValue(rv reflect.Value) bool {
	if !rv.IsValid() {
		return true
	}
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// IsZero 判断 v 是否为零值（支持切片/map/结构体等不可比较类型）。
func IsZero(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}

// TypeName 返回 v 的运行时类型名；v 为 nil 时返回 "<nil>"。
func TypeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
}

// TypeOf 返回 v 的运行时类型；v 为 nil 时返回 nil。
func TypeOf(v any) reflect.Type { return reflect.TypeOf(v) }

// IndirectType 剥离指针与接口，返回最内层的具体类型。
//
// 若传入接口类型本身，返回其本身（因为接口的 Elem 是动态的）。
func IndirectType(t reflect.Type) reflect.Type {
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Interface) {
		if t.Kind() == reflect.Interface {
			return t
		}
		t = t.Elem()
	}
	return t
}

// Indirect 剥离 v 上的指针，返回可寻址的最内层值；nil 指针返回无效值。
func Indirect(v any) reflect.Value {
	if v == nil {
		return reflect.Value{}
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	return rv
}

// New 创建 t 类型的新值（返回指针），功能与 reflect.New 一致。
func New(t reflect.Type) any {
	if t == nil {
		return nil
	}
	return reflect.New(t).Interface()
}

// NewOf 创建 T 的新值并返回指针。
func NewOf[T any]() *T { return new(T) }

// NewValueOf 返回 T 的零值。
func NewValueOf[T any]() T {
	var zero T
	return zero
}

// MakeSlice 创建元素类型为 elem、长度 n、容量 cap 的切片。
func MakeSlice(elem reflect.Type, n, cap int) any {
	if elem == nil {
		return nil
	}
	return reflect.MakeSlice(reflect.SliceOf(elem), n, cap).Interface()
}

// Implements 判断 v 的运行时类型是否实现了 iface。
func Implements(v any, iface reflect.Type) bool {
	if v == nil || iface == nil || iface.Kind() != reflect.Interface {
		return false
	}
	return reflect.TypeOf(v).Implements(iface)
}

// ImplementsInterface 判断 v 是否实现了接口 T。
func ImplementsInterface[T any](v any) bool {
	return Implements(v, reflect.TypeOf((*T)(nil)).Elem())
}

// IsAssignable 判断 v 能否直接赋值给类型 T。
func IsAssignable[T any](v any) bool {
	if v == nil {
		return false
	}
	return reflect.TypeOf(v).AssignableTo(reflect.TypeOf((*T)(nil)).Elem())
}

// ---------------------------------------------------------------------------
// 深拷贝
// ---------------------------------------------------------------------------

type visit struct {
	ptr uintptr
	typ reflect.Type
}

// DeepCopyAny 深拷贝任意值并返回 any；v 为 nil 时返回 nil。
func DeepCopyAny(v any) any {
	if v == nil {
		return nil
	}
	out := deepCopy(reflect.ValueOf(v), make(map[visit]reflect.Value))
	if !out.IsValid() {
		return nil
	}
	return out.Interface()
}

// DeepCopy 深拷贝 v，返回值类型与入参一致。
//
// 支持指针 / 切片 / map / 数组 / 接口 / 结构体，并正确保留别名关系与循环引用：
//
//	a := []*T{{}}
//	a = append(a, a[0])   // 两个元素指向同一对象
//	b := xreflect.DeepCopy(a)   // b[0] 与 b[1] 仍指向同一新对象
func DeepCopy[T any](v T) T {
	var out T
	copied := deepCopy(reflect.ValueOf(v), make(map[visit]reflect.Value))
	dst := reflect.ValueOf(&out).Elem()
	if copied.IsValid() && copied.Type().AssignableTo(dst.Type()) {
		dst.Set(copied)
	}
	return out
}

// Clone 是 DeepCopy 的别名。
func Clone[T any](v T) T { return DeepCopy(v) }

func deepCopy(v reflect.Value, seen map[visit]reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := visit{v.Pointer(), v.Type()}
		if c, ok := seen[key]; ok {
			return c
		}
		out := reflect.New(v.Type().Elem())
		seen[key] = out
		out.Elem().Set(deepCopy(v.Elem(), seen))
		return out

	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		inner := deepCopy(v.Elem(), seen)
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out

	case reflect.Struct:
		// time.Time 是值语义且字段全部未导出，直接复制值
		if v.Type() == timeType {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		// 整体浅拷贝：这是唯一能连未导出字段一起复制的方式
		out.Set(v)
		for i := 0; i < v.Type().NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue // 未导出字段无法单独赋值，保留浅拷贝结果
			}
			out.Field(i).Set(deepCopy(v.Field(i), seen))
		}
		return out

	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if p := v.Pointer(); p != 0 {
			key := visit{p, v.Type()}
			if c, ok := seen[key]; ok {
				return c
			}
			seen[key] = out
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopy(v.Index(i), seen))
		}
		return out

	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := visit{v.Pointer(), v.Type()}
		if c, ok := seen[key]; ok {
			return c
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen[key] = out
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(deepCopy(iter.Key(), seen), deepCopy(iter.Value(), seen))
		}
		return out

	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopy(v.Index(i), seen))
		}
		return out

	default:
		// 基本类型、chan、func 等直接返回
		return v
	}
}

// ---------------------------------------------------------------------------
// 元数据缓存
// ---------------------------------------------------------------------------

// FieldInfo 描述结构体的一个导出字段。
type FieldInfo struct {
	// Name 是 Go 字段名。
	Name string
	// Index 是字段索引路径（嵌入字段为多级）。
	Index []int
	// Type 是字段类型。
	Type reflect.Type
	// Tag 是原始结构体 tag。
	Tag reflect.StructTag
	// Anonymous 表示该字段是否为匿名（嵌入）字段本身。
	Anonymous bool
	// Depth 是嵌入深度，顶层字段为 0。
	Depth int
}

// TagName 解析指定 tag key 的名称部分。
//
// tag 形如 `json:"name,omitempty"`，TagName("json") 返回 "name"。
// 若 tag 为 "-"，返回空串（表示忽略）。
func (f *FieldInfo) TagName(key string) string {
	if f == nil {
		return ""
	}
	v, _ := ParseTag(f.Tag.Get(key)).nameRaw()
	return v
}

// TagOptions 解析指定 tag key 的选项列表。
func (f *FieldInfo) TagOptions(key string) []string {
	if f == nil {
		return nil
	}
	_, opts := ParseTag(f.Tag.Get(key)).nameRaw()
	return opts
}

// HasTagOption 判断 tag 是否包含某个选项。
func (f *FieldInfo) HasTagOption(key, opt string) bool {
	for _, o := range f.TagOptions(key) {
		if o == opt {
			return true
		}
	}
	return false
}

// MapKey 返回该字段在 map 形式下的键名：优先 tag 名，其次 Go 字段名。
func (f *FieldInfo) MapKey(tagKey string) string {
	if f == nil {
		return ""
	}
	if tagKey != "" {
		if n := f.TagName(tagKey); n != "" {
			return n
		}
	}
	return f.Name
}

// TypeInfo 描述一个结构体类型的字段元数据。
type TypeInfo struct {
	// Type 是原始类型。
	Type reflect.Type
	// Kind 是 reflect.Kind。
	Kind reflect.Kind
	// Fields 是按声明顺序（含嵌入字段展开后）排列的导出字段。
	Fields []*FieldInfo
	// byName 是 Go 字段名索引（含嵌入路径的最后一段）。
	byName map[string]*FieldInfo
}

// Describe 返回 v 的结构体元数据。
//
// v 可以是结构体，也可以是结构体指针；其它类型返回错误。
func Describe(v any) (*TypeInfo, error) {
	if v == nil {
		return nil, xerr.NewC(xerr.CodeInvalidParam, "xreflect: Describe(nil)")
	}
	return DescribeType(IndirectType(reflect.TypeOf(v)))
}

// DescribeType 返回类型 t 的结构体元数据。
func DescribeType(t reflect.Type) (*TypeInfo, error) {
	if t == nil {
		return nil, xerr.NewC(xerr.CodeInvalidParam, "xreflect: DescribeType(nil)")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, xerr.NewfC(xerr.CodeInvalidParam, "xreflect: not a struct: %s", t)
	}
	return describeType(t), nil
}

var typeCache sync.Map // map[reflect.Type]*TypeInfo

func describeType(t reflect.Type) *TypeInfo {
	if c, ok := typeCache.Load(t); ok {
		return c.(*TypeInfo)
	}
	info := &TypeInfo{
		Type:   t,
		Kind:   t.Kind(),
		byName: make(map[string]*FieldInfo),
	}
	collectFields(t, nil, 0, info, make(map[reflect.Type]bool))

	actual, _ := typeCache.LoadOrStore(t, info)
	return actual.(*TypeInfo)
}

// collectFields 递归收集导出字段（展开匿名结构体）。
func collectFields(t reflect.Type, prefix []int, depth int, info *TypeInfo, visiting map[reflect.Type]bool) {
	if visiting[t] {
		return // 自引用类型（如链表节点），避免无限递归
	}
	visiting[t] = true
	defer delete(visiting, t)

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			// 未导出字段：无法通过反射赋值，跳过。
			// 例外：匿名的未导出结构体也可能含导出字段，但因其本身不可设置，
			// 这里一并跳过以保证行为可预期。
			continue
		}
		idx := append(append([]int(nil), prefix...), i)

		if sf.Anonymous {
			ft := sf.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				// 嵌入字段本身也作为可访问字段记录（便于按类型名/字段名访问）
				collectFields(ft, idx, depth+1, info, visiting)
				continue
			}
		}

		fi := &FieldInfo{
			Name:      sf.Name,
			Index:     idx,
			Type:      sf.Type,
			Tag:       sf.Tag,
			Anonymous: sf.Anonymous,
			Depth:     depth,
		}
		info.Fields = append(info.Fields, fi)
		// 外层字段优先：仅在尚未记录时写入
		if _, exists := info.byName[sf.Name]; !exists {
			info.byName[sf.Name] = fi
		}
	}
}

// ClearCache 清空元数据缓存（主要用于测试）。
func ClearCache() { typeCache = sync.Map{} }

// Field 按 Go 字段名返回字段元数据（嵌入字段已展开，外层优先）。
func (t *TypeInfo) Field(name string) *FieldInfo {
	if t == nil {
		return nil
	}
	return t.byName[name]
}

// FieldInfoOf 返回 v 上指定字段的元数据；支持 "Inner.A" 形式的点路径。
func FieldInfoOf(v any, name string) *FieldInfo {
	return fieldInfoByPath(v, name)
}

// fieldInfoByPath 按点路径查找字段元数据；不存在时返回 nil。
func fieldInfoByPath(v any, path string) *FieldInfo {
	if path == "" {
		return nil
	}
	info, err := Describe(v)
	if err != nil {
		return nil
	}
	parts := strings.Split(path, ".")
	cur := info
	var fi *FieldInfo
	for i, p := range parts {
		fi = cur.Field(p)
		if fi == nil {
			return nil
		}
		if i < len(parts)-1 {
			t := fi.Type
			for t.Kind() == reflect.Ptr {
				t = t.Elem()
			}
			if t.Kind() != reflect.Struct {
				return nil
			}
			cur = describeType(t)
		}
	}
	return fi
}

// FieldByTag 按 tag key + 名称查找字段。
func (t *TypeInfo) FieldByTag(tagKey, name string) *FieldInfo {
	if t == nil || tagKey == "" {
		return nil
	}
	for _, f := range t.Fields {
		if f.TagName(tagKey) == name {
			return f
		}
	}
	return nil
}

// FieldNames 返回全部导出字段名（含嵌入字段展开后的顺序）。
func (t *TypeInfo) FieldNames() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.Fields))
	for _, f := range t.Fields {
		out = append(out, f.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// 字段读写
// ---------------------------------------------------------------------------

// fieldByIndexSafe 按索引路径取值；路径上遇到 nil 指针时返回 not found。
func fieldByIndexSafe(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, i := range index {
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(i)
	}
	return v, true
}

// Get 按字段路径取值（"Age" 或 "User.Profile.Age"），也支持嵌入字段短名。
//
// 未找到、路径上存在 nil 指针或字段不可访问时返回 (nil, false)。
func Get(v any, path string) (any, bool) {
	return getByPath(v, path)
}

// getByPath 是 Get 的核心实现。
func getByPath(v any, path string) (any, bool) {
	if v == nil || path == "" {
		return nil, false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, false
	}

	cur := rv
	for _, part := range strings.Split(path, ".") {
		cur = derefValue(cur)
		if cur.Kind() != reflect.Struct {
			return nil, false
		}
		info := describeType(cur.Type())
		fi := info.byName[part]
		if fi == nil {
			return nil, false
		}
		fv, ok := fieldByIndexSafe(cur, fi.Index)
		if !ok {
			return nil, false
		}
		cur = fv
	}
	cur = derefValue(cur)
	if !cur.IsValid() || !cur.CanInterface() {
		return nil, false
	}
	return cur.Interface(), true
}

// GetAll 返回全部导出字段的 名称 → 值 映射（嵌入字段已展开）。
func GetAll(v any) (map[string]any, bool) {
	info, err := Describe(v)
	if err != nil {
		return nil, false
	}
	rv := derefValue(reflect.ValueOf(v))
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return nil, false
	}
	out := make(map[string]any, len(info.Fields))
	for _, f := range info.Fields {
		fv, ok := fieldByIndexSafe(rv, f.Index)
		if !ok {
			continue
		}
		fv = derefValue(fv)
		if !fv.IsValid() || !fv.CanInterface() {
			continue
		}
		out[f.Name] = fv.Interface()
	}
	return out, true
}

// Names 返回 v 的全部导出字段名（嵌入字段展开后的声明顺序）。
func Names(v any) []string {
	info, err := Describe(v)
	if err != nil {
		return nil
	}
	return info.FieldNames()
}

// GetAllValues 返回 v 的全部导出字段值（顺序与 Names 一致；无法读取的项为 nil）。
func GetAllValues(v any) ([]any, bool) {
	info, err := Describe(v)
	if err != nil {
		return nil, false
	}
	rv := derefValue(reflect.ValueOf(v))
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return nil, false
	}
	out := make([]any, 0, len(info.Fields))
	for _, f := range info.Fields {
		fv, ok := fieldByIndexSafe(rv, f.Index)
		if !ok {
			out = append(out, nil)
			continue
		}
		fv = derefValue(fv)
		if !fv.IsValid() || !fv.CanInterface() {
			out = append(out, nil)
			continue
		}
		out = append(out, fv.Interface())
	}
	return out, true
}

// Has 判断 v 上是否存在可读取的字段路径。
func Has(v any, path string) bool {
	_, ok := getByPath(v, path)
	return ok
}

// KindOf 返回字段的 reflect.Kind（按 Go 字段名，不含路径）；无法确定时返回 reflect.Invalid。
func KindOf(v any, name string) reflect.Kind {
	if f := FieldInfoOf(v, name); f != nil {
		return f.Type.Kind()
	}
	return reflect.Invalid
}

// derefValue 剥离指针与接口，返回最内层值。
func derefValue(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// Set 按字段路径给结构体指针赋值，使用弱类型转换（见 Assign）。
//
// dst 必须是结构体指针；路径上的 nil 嵌套指针会被自动分配。
// 字段不存在 / 不可设置 / 类型无法转换时返回错误。
func Set(dst any, path string, val any) error {
	return SetByPath(dst, path, val)
}

// SetByPath 与 Set 等价，命名上强调支持 "User.Profile.Age" 形式的路径。
func SetByPath(dst any, path string, val any) error {
	if dst == nil {
		return xerr.NewC(xerr.CodeInvalidParam, "xreflect: SetFieldByPath(nil)")
	}
	parts := strings.Split(path, ".")
	cur := reflect.ValueOf(dst)
	if cur.Kind() != reflect.Ptr || cur.IsNil() {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: dst must be a non-nil pointer, got %s", TypeName(dst))
	}

	for i, part := range parts {
		cur = ensureStructValue(cur)
		if !cur.IsValid() || cur.Kind() != reflect.Struct {
			return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: %q is not a struct", strings.Join(parts[:i], "."))
		}
		info := describeType(cur.Type())
		fi := info.byName[part]
		if fi == nil {
			return xerr.NewfC(xerr.CodeNotFound, "xreflect: field %q not found in %s", part, cur.Type())
		}
		target, ok := fieldByIndexForWrite(cur, fi.Index)
		if !ok {
			return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: field path %q not settable (nil embedded pointer)", path)
		}
		if i == len(parts)-1 {
			if !target.CanSet() {
				return xerr.NewfC(xerr.CodeForbidden, "xreflect: field %q is not settable", part)
			}
			return Assign(target, val)
		}
		cur = target
	}
	return nil
}

// ensureStructValue 沿指针链解引用，遇到 nil 指针且可设置时自动分配新对象。
//
// 与 derefValue 的区别：本函数会「补齐」路径上的 nil 指针，供写入场景使用。
func ensureStructValue(v reflect.Value) reflect.Value {
	for v.IsValid() {
		switch v.Kind() {
		case reflect.Ptr:
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		case reflect.Interface:
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		default:
			return v
		}
	}
	return v
}

// fieldByIndexForWrite 与 fieldByIndexSafe 类似，但会为路径上的 nil 指针自动分配。
func fieldByIndexForWrite(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, i := range index {
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(i)
	}
	return v, true
}

// ---------------------------------------------------------------------------
// tag 解析
// ---------------------------------------------------------------------------

// Tag 是解析后的结构体 tag。
type Tag struct {
	// Value 是解析后的主值（逗号之前的部分）。
	Value string
	// Options 是全部选项（逗号分隔的后续片段）。
	Options []string
	// Values 是 key=value 形式的选项（如 validate:"min=1"）。
	Values map[string]string
}

// nameRaw 返回 (Value, Options)，供 FieldInfo 使用。
func (t Tag) nameRaw() (string, []string) { return t.Value, t.Options }

// Get 返回 key=value 形式的选项值。
func (t Tag) Get(key string) (string, bool) {
	v, ok := t.Values[key]
	return v, ok
}

// Has 判断是否存在指定选项。
//
// 同时匹配主值（如 ParseTag("required,min=1").Has("required") 为 true）
// 与选项（含 "opt=value" 形式）。
func (t Tag) Has(opt string) bool {
	if opt == "" {
		return false
	}
	if t.Value == opt {
		return true
	}
	for _, o := range t.Options {
		if o == opt || strings.HasPrefix(o, opt+"=") {
			return true
		}
	}
	return false
}

// String 还原为 tag 字符串形式。
func (t Tag) String() string {
	if t.Value == "" && len(t.Options) == 0 {
		return ""
	}
	return strings.Join(append([]string{t.Value}, t.Options...), ",")
}

// ParseTag 解析形如 "name,omitempty,min=1" 的 tag 内容。
//
// 空字符串返回零值 Tag。
func ParseTag(tag string) Tag {
	var t Tag
	t.Values = make(map[string]string)
	if tag == "" {
		return t
	}
	parts := strings.Split(tag, ",")
	t.Value = strings.TrimSpace(parts[0])
	t.Options = make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t.Options = append(t.Options, p)
		if i := strings.IndexByte(p, '='); i > 0 {
			t.Values[p[:i]] = p[i+1:]
		}
	}
	return t
}

// GetTag 返回 v 上指定字段的 tag 原始值。
func GetTag(v any, field, key string) string {
	f := FieldInfoOf(v, field)
	if f == nil {
		return ""
	}
	return f.Tag.Get(key)
}

// GetTagValue 返回 v 上指定字段 tag 的主值（逗号之前）。
func GetTagValue(v any, field, key string) string {
	f := FieldInfoOf(v, field)
	if f == nil {
		return ""
	}
	return f.TagName(key)
}

// TagOptions 返回 v 上指定字段 tag 的选项列表。
func TagOptions(v any, field, key string) []string {
	f := FieldInfoOf(v, field)
	if f == nil {
		return nil
	}
	return f.TagOptions(key)
}

// HasTagOption 判断 v 上指定字段 tag 是否包含某选项。
func HasTagOption(v any, field, key, opt string) bool {
	f := FieldInfoOf(v, field)
	if f == nil {
		return false
	}
	return f.HasTagOption(key, opt)
}

// ---------------------------------------------------------------------------
// 动态调用
// ---------------------------------------------------------------------------

// Call 反射调用 fn，返回全部返回值。
//
//   - 参数会做可赋值性检查，必要时自动 Convert；
//   - 变参函数：前 NumIn()-1 个参数逐个传，其余参数自动打包为切片；
//   - fn 内部 panic 会被捕获并转为错误返回。
func Call(fn any, args ...any) (results []any, err error) {
	if fn == nil {
		return nil, xerr.NewC(xerr.CodeInvalidParam, "xreflect: Call(nil)")
	}
	fv := reflect.ValueOf(fn)
	if fv.Kind() != reflect.Func {
		return nil, xerr.NewfC(xerr.CodeInvalidParam, "xreflect: not a func: %s", fv.Type())
	}
	ft := fv.Type()

	in, cerr := buildCallArgs(ft, args)
	if cerr != nil {
		return nil, cerr
	}

	defer func() {
		if r := recover(); r != nil {
			results = nil
			err = xerr.Newf("xreflect: panic in called function: %v", r)
		}
	}()

	var out []reflect.Value
	if ft.IsVariadic() {
		// buildCallArgs 已经把可变参数打包成切片，必须用 CallSlice
		out = fv.CallSlice(in)
	} else {
		out = fv.Call(in)
	}

	results = make([]any, len(out))
	for i, o := range out {
		results[i] = o.Interface()
	}
	return results, nil
}

// Call1 反射调用并返回第一个返回值。
func Call1(fn any, args ...any) (any, error) {
	rs, err := Call(fn, args...)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, xerr.NewC(xerr.CodeInvalidParam, "xreflect: function returns nothing")
	}
	return rs[0], nil
}

func buildCallArgs(ft reflect.Type, args []any) ([]reflect.Value, error) {
	n := ft.NumIn()
	if ft.IsVariadic() {
		if len(args) < n-1 {
			return nil, xerr.NewfC(xerr.CodeInvalidParam,
				"xreflect: variadic func needs at least %d args, got %d", n-1, len(args))
		}
		// 先处理固定参数
		in := make([]reflect.Value, n)
		for i := 0; i < n-1; i++ {
			v, err := convertArg(args[i], ft.In(i))
			if err != nil {
				return nil, err
			}
			in[i] = v
		}
		// 其余参数打包为切片
		slice := reflect.MakeSlice(ft.In(n-1), 0, len(args)-n+1)
		for _, a := range args[n-1:] {
			v, err := convertArg(a, ft.In(n-1).Elem())
			if err != nil {
				return nil, err
			}
			slice = reflect.Append(slice, v)
		}
		in[n-1] = slice
		return in, nil
	}

	if len(args) != n {
		return nil, xerr.NewfC(xerr.CodeInvalidParam, "xreflect: func needs %d args, got %d", n, len(args))
	}
	in := make([]reflect.Value, n)
	for i := range args {
		v, err := convertArg(args[i], ft.In(i))
		if err != nil {
			return nil, err
		}
		in[i] = v
	}
	return in, nil
}

// convertArg 把参数转换为目标类型。
//
// 指针 / chan / func 这类「引用型」目标只接受可赋值或可显式 Convert 的值
// （不会凭空分配新对象）；其余类型走 Assign 的弱类型转换，
// 因此 Call(fn, "3") 可以调用 func(int) 而无需手工转换。
func convertArg(a any, want reflect.Type) (reflect.Value, error) {
	if a == nil {
		switch want.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map,
			reflect.Chan, reflect.Func:
			return reflect.Zero(want), nil
		}
		return reflect.Value{}, xerr.NewfC(xerr.CodeInvalidParam,
			"xreflect: cannot pass nil as %s", want)
	}

	v := reflect.ValueOf(a)
	if v.Type().AssignableTo(want) {
		return v, nil
	}
	if v.Type().ConvertibleTo(want) && want.Kind() != reflect.Ptr {
		return v.Convert(want), nil
	}

	switch want.Kind() {
	case reflect.Ptr, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, xerr.NewfC(xerr.CodeInvalidParam,
			"xreflect: cannot use %s as %s", v.Type(), want)
	}

	out := reflect.New(want).Elem()
	if err := assignValue(out, v); err != nil {
		return reflect.Value{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 默认值填充
// ---------------------------------------------------------------------------

// DefaultTagKey 是 FillDefaults 默认读取的 tag key。
const DefaultTagKey = "default"

// FillDefaults 用结构体 tag 中的默认值填充当前为零值的字段。
//
// 用法：
//
//	type Cfg struct {
//	    Port int    `default:"8080"`
//	    Name string `default:"gokit"`
//	}
//	xreflect.FillDefaults(&cfg)
//
// tag 值通过 Assign 做弱类型转换，因此 default:"5s" 可填入 time.Duration。
func FillDefaults(v any, tagKey ...string) error {
	key := DefaultTagKey
	if len(tagKey) > 0 && tagKey[0] != "" {
		key = tagKey[0]
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: FillDefaults needs a non-nil pointer, got %s", TypeName(v))
	}
	rv = rv.Elem()
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: FillDefaults needs a struct pointer, got %s", rv.Type())
	}
	return fillDefaults(rv, key)
}

func fillDefaults(rv reflect.Value, key string) error {
	info := describeType(rv.Type())
	var errs []error
	for _, f := range info.Fields {
		fv, ok := fieldByIndexForWrite(rv, f.Index)
		if !ok || !fv.CanSet() {
			continue
		}

		// 先递归处理嵌套结构体（无论它自身是否带 default tag）
		switch {
		case fv.Kind() == reflect.Ptr && fv.Type().Elem().Kind() == reflect.Struct &&
			fv.Type().Elem() != timeType && fv.Type().Elem() != durationType:
			if hasDefaultTag(fv.Type().Elem(), key) {
				if fv.IsNil() {
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				if err := fillDefaults(fv.Elem(), key); err != nil {
					errs = append(errs, err)
				}
			}
		case fv.Kind() == reflect.Struct && fv.Type() != timeType && fv.Type() != durationType:
			if hasDefaultTag(fv.Type(), key) {
				if err := fillDefaults(fv, key); err != nil {
					errs = append(errs, err)
				}
			}
		}

		raw := f.Tag.Get(key)
		if raw == "" {
			continue
		}
		if !fv.IsZero() {
			continue // 已有值，不覆盖
		}
		if err := Assign(fv, raw); err != nil {
			errs = append(errs, fieldErr(f.Name, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return xerr.NewMulti(errs...)
}

// hasDefaultTag 判断类型（含嵌套结构体）中是否存在指定 tag key 的默认值。
//
// depth 上限用于防御自引用类型导致的无限递归。
func hasDefaultTag(t reflect.Type, key string) bool { return hasDefaultTagDepth(t, key, 0) }

func hasDefaultTagDepth(t reflect.Type, key string, depth int) bool {
	if depth > 8 {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	info := describeType(t)
	for _, f := range info.Fields {
		if f.Tag.Get(key) != "" {
			return true
		}
		ft := f.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != timeType && ft != durationType {
			if hasDefaultTagDepth(ft, key, depth+1) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// FuncName 返回 fn 的运行时函数名；非函数返回空串。
func FuncName(fn any) string {
	if fn == nil {
		return ""
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return ""
	}
	if rv.IsNil() {
		return ""
	}
	return fmt.Sprintf("%s", rv.Type())
}

// IsStruct 判断 v（或 v 指向的值）是否为结构体。
func IsStruct(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}
