package xreflect

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/indigo-apple/gokit/xerr"
	"github.com/indigo-apple/gokit/xtime"
)

// ---------------------------------------------------------------------------
// Assign
// ---------------------------------------------------------------------------

// Assign 把一个任意值弱类型地赋给 dst（dst 必须可设置）。
//
// 支持的转换：
//
//   - 同类型 / 可赋值类型：直接赋值
//   - 目标为指针：必要时分配新对象后递归赋值
//   - 源为指针 / 接口：自动解引用（nil 则写入零值）
//   - string ↔ 数值 ↔ bool：双向转换（字符串空串视为零值，无法解析时报错）
//   - []byte ← string
//   - 切片 / 数组 ← 切片 / 数组：逐元素递归赋值（长度不同时报错）
//   - 切片 ← string：按逗号切分（去空白、丢空段），适配 env / 表单 / CSV 场景
//   - map：逐键值递归赋值；string 键的 map 也可由 struct 转换而来
//   - 结构体 ← 结构体：按字段名（其次 json tag 名）匹配
//   - 结构体 ← map：按 json tag 名（其次 Go 字段名）匹配
//   - time.Time ← string / 数字（秒/毫秒/微秒/纳秒自动识别）
//   - time.Duration ← string（"1h30m"）/ 数字（纳秒）
//
// 数值收窄（如 int64 → int8）按 Go 语义**截断**，不做溢出报错。
func Assign(dst reflect.Value, src any) error {
	if !dst.IsValid() {
		return xerr.NewC(xerr.CodeInvalidParam, "xreflect: Assign to an invalid value")
	}
	if !dst.CanSet() {
		return xerr.NewC(xerr.CodeForbidden, "xreflect: destination is not settable")
	}
	if src == nil {
		dst.SetZero()
		return nil
	}
	return assignValue(dst, reflect.ValueOf(src))
}

// SetValue 把 src 写入 dst 指向的值。
//
//	dst 必须是非 nil 指针。等价于对 *dst 调用 Assign。
func SetValue(dst any, src any) error {
	if dst == nil {
		return xerr.NewC(xerr.CodeInvalidParam, "xreflect: SetValue(nil)")
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: SetValue needs a non-nil pointer, got %s", TypeName(dst))
	}
	return Assign(rv.Elem(), src)
}

// fieldErr 为字段级转换错误附加上下文（消息可读 + Meta 可结构化提取）。
func fieldErr(field string, err error) error {
	return xerr.WithMeta(xerr.Wrapf(err, "xreflect: field %q", field), "field", field)
}

// indexErr 为切片 / 数组元素级错误附加上下文。
func indexErr(i int, err error) error {
	return xerr.WithMeta(xerr.Wrapf(err, "xreflect: index %d", i), "index", i)
}

// keyErr 为 map 键值级错误附加上下文。
func keyErr(key any, err error) error {
	return xerr.WithMeta(xerr.Wrapf(err, "xreflect: map key %v", key), "map_key", key)
}

func typeErr(src, dst reflect.Value) error {
	srcName := "<invalid>"
	if src.IsValid() {
		srcName = src.Type().String()
	}
	dstName := "<invalid>"
	if dst.IsValid() {
		dstName = dst.Type().String()
	}
	return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: cannot assign %s to %s", srcName, dstName)
}

func assignValue(dst, src reflect.Value) error {
	if !src.IsValid() {
		dst.SetZero()
		return nil
	}

	// 目标为接口：可赋值则直接放入
	if dst.Kind() == reflect.Interface {
		if src.Type().AssignableTo(dst.Type()) {
			dst.Set(src)
			return nil
		}
		if dst.NumMethod() == 0 {
			dst.Set(src)
			return nil
		}
		return typeErr(src, dst)
	}

	// 目标为指针：必要时分配
	if dst.Kind() == reflect.Ptr {
		if src.Kind() == reflect.Ptr {
			if src.IsNil() {
				dst.SetZero()
				return nil
			}
			if src.Type().AssignableTo(dst.Type()) {
				dst.Set(src)
				return nil
			}
			src = src.Elem()
		}
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return assignValue(dst.Elem(), src)
	}

	// 源为指针 / 接口：解引用
	for src.Kind() == reflect.Ptr || src.Kind() == reflect.Interface {
		if src.IsNil() {
			dst.SetZero()
			return nil
		}
		src = src.Elem()
	}

	// 同类型 / 可赋值
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return nil
	}

	// 特殊类型优先
	if dst.Type() == timeType {
		return assignTime(dst, src)
	}
	if dst.Type() == durationType {
		return assignDuration(dst, src)
	}

	switch dst.Kind() {
	case reflect.String:
		if s, ok := toStr(src); ok {
			dst.SetString(s)
			return nil
		}
	case reflect.Bool:
		if b, ok := toBool(src); ok {
			dst.SetBool(b)
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok := toInt(src); ok {
			dst.SetInt(n)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if n, ok := toUint(src); ok {
			dst.SetUint(n)
			return nil
		}
	case reflect.Float32, reflect.Float64:
		if f, ok := toFloat(src); ok {
			dst.SetFloat(f)
			return nil
		}
	case reflect.Slice:
		return assignSlice(dst, src)
	case reflect.Array:
		return assignArray(dst, src)
	case reflect.Map:
		return assignMap(dst, src)
	case reflect.Struct:
		return assignStruct(dst, src)
	}
	return typeErr(src, dst)
}

// ---------------------------------------------------------------------------
// 标量转换
// ---------------------------------------------------------------------------

func toStr(src reflect.Value) (string, bool) {
	// 时间与时长必须先特判：它们的底层 Kind 分别是 struct / int64，
	// 否则 time.Duration 会被格式化成纳秒整数（"90000000000"）而不是 "1m30s"。
	if src.Type() == durationType && src.CanInterface() {
		if d, ok := src.Interface().(time.Duration); ok {
			return d.String(), true
		}
	}
	if src.Type() == timeType && src.CanInterface() {
		if t, ok := src.Interface().(time.Time); ok {
			return t.Format(time.RFC3339Nano), true
		}
	}

	switch src.Kind() {
	case reflect.String:
		return src.String(), true
	case reflect.Bool:
		return strconv.FormatBool(src.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(src.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(src.Uint(), 10), true
	case reflect.Float32:
		return strconv.FormatFloat(src.Float(), 'f', -1, 32), true
	case reflect.Float64:
		return strconv.FormatFloat(src.Float(), 'f', -1, 64), true
	case reflect.Slice:
		if src.Type() == byteSliceTyp {
			return string(src.Bytes()), true
		}
	case reflect.Invalid:
		return "", true
	}
	if src.CanInterface() {
		if s, ok := src.Interface().(interface{ String() string }); ok {
			return s.String(), true
		}
	}
	return "", false
}

func toInt(src reflect.Value) (int64, bool) {
	switch src.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return src.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(src.Uint()), true
	case reflect.Float32, reflect.Float64:
		return int64(src.Float()), true
	case reflect.Bool:
		if src.Bool() {
			return 1, true
		}
		return 0, true
	case reflect.String:
		// 空串视为零值（与「未设置」语义一致）
		s := strings.TrimSpace(src.String())
		if s == "" {
			return 0, true
		}
		if n, err := strconv.ParseInt(s, 0, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
		if b, err := strconv.ParseBool(s); err == nil {
			if b {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

func toUint(src reflect.Value) (uint64, bool) {
	switch src.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return src.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := src.Int()
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case reflect.Float32, reflect.Float64:
		f := src.Float()
		if f < 0 {
			return 0, false
		}
		return uint64(f), true
	case reflect.Bool:
		if src.Bool() {
			return 1, true
		}
		return 0, true
	case reflect.String:
		s := strings.TrimSpace(src.String())
		if s == "" {
			return 0, true
		}
		if n, err := strconv.ParseUint(s, 0, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 {
			return uint64(f), true
		}
	}
	return 0, false
}

func toFloat(src reflect.Value) (float64, bool) {
	switch src.Kind() {
	case reflect.Float32, reflect.Float64:
		return src.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(src.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(src.Uint()), true
	case reflect.Bool:
		if src.Bool() {
			return 1, true
		}
		return 0, true
	case reflect.String:
		s := strings.TrimSpace(src.String())
		if s == "" {
			return 0, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// boolTrueValues / boolFalseValues 定义字符串 → bool 的宽松映射。
var (
	boolTrueValues  = []string{"true", "1", "yes", "y", "on", "t"}
	boolFalseValues = []string{"false", "0", "no", "n", "off", "f"}
)

func toBool(src reflect.Value) (bool, bool) {
	switch src.Kind() {
	case reflect.Bool:
		return src.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return src.Int() != 0, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return src.Uint() != 0, true
	case reflect.Float32, reflect.Float64:
		return src.Float() != 0, true
	case reflect.String:
		s := strings.ToLower(strings.TrimSpace(src.String()))
		for _, v := range boolTrueValues {
			if s == v {
				return true, true
			}
		}
		for _, v := range boolFalseValues {
			if s == v {
				return false, true
			}
		}
		if s == "" {
			return false, true
		}
		if b, err := strconv.ParseBool(s); err == nil {
			return b, true
		}
	}
	return false, false
}

// ---------------------------------------------------------------------------
// 复合类型
// ---------------------------------------------------------------------------

func assignSlice(dst, src reflect.Value) error {
	elemKind := dst.Type().Elem().Kind()

	// []byte ← string / []byte
	if elemKind == reflect.Uint8 {
		switch {
		case src.Kind() == reflect.String:
			dst.SetBytes([]byte(src.String()))
			return nil
		case src.Kind() == reflect.Slice && src.Type().Elem().Kind() == reflect.Uint8:
			dst.SetBytes(src.Bytes())
			return nil
		case src.Kind() == reflect.Array && src.Type().Elem().Kind() == reflect.Uint8:
			b := make([]byte, src.Len())
			for i := 0; i < src.Len(); i++ {
				b[i] = byte(src.Index(i).Uint())
			}
			dst.SetBytes(b)
			return nil
		}
	}

	if src.Kind() != reflect.Slice && src.Kind() != reflect.Array {
		// string → 切片：按逗号切分并去空白 / 丢空段。
		// 这是 env 变量、表单、CSV 单元格写切片的通用约定。
		if src.Kind() == reflect.String {
			parts := strings.Split(src.String(), ",")
			out := reflect.MakeSlice(dst.Type(), 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				elem := reflect.New(dst.Type().Elem()).Elem()
				if err := assignValue(elem, reflect.ValueOf(p)); err != nil {
					return err
				}
				out = reflect.Append(out, elem)
			}
			dst.Set(out)
			return nil
		}
		return typeErr(src, dst)
	}
	out := reflect.MakeSlice(dst.Type(), src.Len(), src.Len())
	for i := 0; i < src.Len(); i++ {
		if err := assignValue(out.Index(i), src.Index(i)); err != nil {
			return indexErr(i, err)
		}
	}
	dst.Set(out)
	return nil
}

func assignArray(dst, src reflect.Value) error {
	if src.Kind() != reflect.Slice && src.Kind() != reflect.Array {
		return typeErr(src, dst)
	}
	if src.Len() != dst.Len() {
		return xerr.NewfC(xerr.CodeInvalidParam,
			"xreflect: array length mismatch: %d -> %d", src.Len(), dst.Len())
	}
	for i := 0; i < dst.Len(); i++ {
		if err := assignValue(dst.Index(i), src.Index(i)); err != nil {
			return indexErr(i, err)
		}
	}
	return nil
}

func assignMap(dst, src reflect.Value) error {
	// map[string]X ← struct：先转成 map
	if src.Kind() == reflect.Struct && dst.Type().Key().Kind() == reflect.String {
		m, err := StructToMap(src.Interface())
		if err != nil {
			return err
		}
		src = reflect.ValueOf(m)
	}
	if src.Kind() != reflect.Map {
		return typeErr(src, dst)
	}
	out := reflect.MakeMapWithSize(dst.Type(), src.Len())
	iter := src.MapRange()
	for iter.Next() {
		k := reflect.New(dst.Type().Key()).Elem()
		if err := assignValue(k, iter.Key()); err != nil {
			return keyErr(safeAny(iter.Key()), err)
		}
		v := reflect.New(dst.Type().Elem()).Elem()
		if err := assignValue(v, iter.Value()); err != nil {
			return keyErr(safeAny(iter.Key()), err)
		}
		out.SetMapIndex(k, v)
	}
	dst.Set(out)
	return nil
}

func assignStruct(dst, src reflect.Value) error {
	dstInfo := describeType(dst.Type())
	var errs []error

	switch src.Kind() {
	case reflect.Struct:
		srcInfo := describeType(src.Type())
		for _, f := range dstInfo.Fields {
			sf := srcInfo.byName[f.Name]
			if sf == nil {
				sf = srcInfo.FieldByTag("json", f.MapKey("json"))
			}
			if sf == nil {
				continue
			}
			sv, ok := fieldByIndexSafe(src, sf.Index)
			if !ok {
				continue
			}
			dv, ok := fieldByIndexForWrite(dst, f.Index)
			if !ok || !dv.CanSet() {
				continue
			}
			if err := assignValue(dv, sv); err != nil {
				errs = append(errs, fieldErr(f.Name, err))
			}
		}

	case reflect.Map:
		for _, f := range dstInfo.Fields {
			mv := lookupMapKey(src, f.MapKey("json"))
			if !mv.IsValid() {
				mv = lookupMapKey(src, f.Name)
			}
			if !mv.IsValid() {
				continue
			}
			dv, ok := fieldByIndexForWrite(dst, f.Index)
			if !ok || !dv.CanSet() {
				continue
			}
			if err := assignValue(dv, mv); err != nil {
				errs = append(errs, fieldErr(f.Name, err))
			}
		}

	default:
		return typeErr(src, dst)
	}

	if len(errs) > 0 {
		return xerr.NewMulti(errs...)
	}
	return nil
}

// lookupMapKey 在字符串键的 map 中按键取值；不存在时返回无效 Value。
func lookupMapKey(m reflect.Value, key string) reflect.Value {
	if m.Kind() != reflect.Map || m.Type().Key().Kind() != reflect.String {
		return reflect.Value{}
	}
	return m.MapIndex(reflect.ValueOf(key).Convert(m.Type().Key()))
}

// safeAny 安全地取出 Value 的接口值（不可取时返回 nil）。
func safeAny(v reflect.Value) any {
	if !v.IsValid() || !v.CanInterface() {
		return nil
	}
	return v.Interface()
}

// ---------------------------------------------------------------------------
// 时间
// ---------------------------------------------------------------------------

func assignTime(dst, src reflect.Value) error {
	switch src.Kind() {
	case reflect.String:
		s := strings.TrimSpace(src.String())
		if s == "" {
			dst.SetZero()
			return nil
		}
		for _, layout := range xtime.Layouts {
			if t, err := time.Parse(layout, s); err == nil {
				dst.Set(reflect.ValueOf(t))
				return nil
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			dst.Set(reflect.ValueOf(xtime.AutoFromTimestamp(n)))
			return nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			dst.Set(reflect.ValueOf(unixFloatToTime(f)))
			return nil
		}
		return xerr.NewfC(xerr.CodeInvalidFormat, "xreflect: cannot parse time from %q", s)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst.Set(reflect.ValueOf(unixToTime(src.Int())))
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		dst.Set(reflect.ValueOf(unixToTime(int64(src.Uint()))))
		return nil

	case reflect.Float32, reflect.Float64:
		dst.Set(reflect.ValueOf(unixFloatToTime(src.Float())))
		return nil
	}
	return typeErr(src, dst)
}

// unixToTime 按数值量级自动识别秒 / 毫秒 / 微秒 / 纳秒。
//
// 复用 xtime.AutoFromTimestamp，保证 xreflect / xconv / xtime 的
// 时间戳识别行为完全一致（单一实现来源）。
func unixToTime(n int64) time.Time { return xtime.AutoFromTimestamp(n) }

func unixFloatToTime(f float64) time.Time {
	return unixToTime(int64(f))
}

func assignDuration(dst, src reflect.Value) error {
	switch src.Kind() {
	case reflect.String:
		s := strings.TrimSpace(src.String())
		if s == "" {
			dst.SetZero()
			return nil
		}
		if d, err := time.ParseDuration(s); err == nil {
			dst.SetInt(int64(d))
			return nil
		}
		// 纯数字字符串按纳秒处理（与 time.Duration 的底层单位一致）
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			dst.SetInt(n)
			return nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			dst.SetInt(int64(f))
			return nil
		}
		return xerr.NewfC(xerr.CodeInvalidFormat, "xreflect: cannot parse duration from %q", s)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst.SetInt(src.Int())
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		dst.SetInt(int64(src.Uint()))
		return nil

	case reflect.Float32, reflect.Float64:
		dst.SetInt(int64(src.Float()))
		return nil
	}
	return typeErr(src, dst)
}

// ---------------------------------------------------------------------------
// struct <-> map
// ---------------------------------------------------------------------------

type mapConfig struct {
	tag       string
	omitEmpty bool
	nested    bool
	useGoName bool
}

// MapOption 配置 StructToMap / MapToStruct 的行为。
type MapOption func(*mapConfig)

// WithTag 指定使用的结构体 tag key（默认 "json"）。
func WithTag(key string) MapOption {
	return func(c *mapConfig) {
		if key != "" {
			c.tag = key
		}
	}
}

// WithOmitEmpty 让零值字段不出现在结果里（默认只遵循 tag 上的 omitempty）。
func WithOmitEmpty() MapOption {
	return func(c *mapConfig) { c.omitEmpty = true }
}

// WithNested 递归把嵌套结构体也转成 map。
func WithNested() MapOption {
	return func(c *mapConfig) { c.nested = true }
}

// WithGoFieldName 忽略 tag，直接使用 Go 字段名作为键。
func WithGoFieldName() MapOption {
	return func(c *mapConfig) { c.useGoName = true }
}

func newMapConfig(opts ...MapOption) *mapConfig {
	cfg := &mapConfig{tag: "json"}
	for _, o := range opts {
		if o != nil {
			o(cfg)
		}
	}
	return cfg
}

// StructToMap 把结构体（或其指针）转为 map[string]any。
//
// 行为：
//   - 只包含导出字段；隐式展开的嵌入字段使用其自身字段名；
//   - tag 为 "-" 的字段被跳过；
//   - tag 含 omitempty / omitzero 时零值字段被跳过；
//   - nil 指针 / nil 接口字段输出为 nil（除非被 omitempty 跳过）；
//   - 非 nil 指针字段自动解引用后输出其指向的值。
func StructToMap(v any, opts ...MapOption) (map[string]any, error) {
	cfg := newMapConfig(opts...)
	info, err := Describe(v)
	if err != nil {
		return nil, err
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return map[string]any{}, nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, xerr.NewfC(xerr.CodeInvalidParam, "xreflect: StructToMap needs a struct, got %s", TypeName(v))
	}

	out := make(map[string]any, len(info.Fields))
	for _, f := range info.Fields {
		key := f.Name
		omit := cfg.omitEmpty

		if !cfg.useGoName && cfg.tag != "" && f.Tag != "" {
			tagName := strings.Split(f.Tag.Get(cfg.tag), ",")[0]
			if tagName == "-" {
				continue
			}
			if tagName != "" {
				key = tagName
			}
			if f.HasTagOption(cfg.tag, "omitempty") || f.HasTagOption(cfg.tag, "omitzero") {
				omit = true
			}
		}

		raw, ok := fieldByIndexSafe(rv, f.Index)
		if !ok {
			continue
		}
		fv := derefValue(raw)
		if !fv.IsValid() {
			// nil 指针 / nil 接口
			if omit {
				continue
			}
			out[key] = nil
			continue
		}
		if omit && fv.IsZero() {
			continue
		}

		val := fv.Interface()
		if cfg.nested && fv.Kind() == reflect.Struct && fv.Type() != timeType {
			sub, serr := StructToMap(val, opts...)
			if serr != nil {
				return nil, xerr.WithMeta(serr, "field", f.Name)
			}
			out[key] = sub
			continue
		}
		out[key] = val
	}
	return out, nil
}

// StructToMapString 把结构体转为 map[string]string。
//
// 标量走字符串化；切片 / map / 嵌套结构体用 JSON 表达（如 `["x","y"]`）；
// 完全无法表达的字段（chan / func 等）会被跳过。
func StructToMapString(v any, opts ...MapOption) (map[string]string, error) {
	m, err := StructToMap(v, opts...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if val == nil {
			out[k] = ""
			continue
		}
		if s, ok := toStr(reflect.ValueOf(val)); ok {
			out[k] = s
			continue
		}
		b, jerr := json.Marshal(val)
		if jerr != nil {
			continue // chan / func 等无法表达，跳过
		}
		out[k] = string(b)
	}
	return out, nil
}

// MapToStruct 把 map 中的值写入 dst 指向的结构体。
//
// 键匹配顺序：tag 名（默认 json）→ Go 字段名。
// 单个字段转换失败不会中断其它字段，最终返回聚合错误。
func MapToStruct(m map[string]any, dst any, opts ...MapOption) error {
	if dst == nil {
		return xerr.NewC(xerr.CodeInvalidParam, "xreflect: MapToStruct(nil)")
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: MapToStruct needs a non-nil pointer, got %s", TypeName(dst))
	}
	if len(m) == 0 {
		return nil
	}

	cfg := newMapConfig(opts...)
	info, err := Describe(dst)
	if err != nil {
		return err
	}

	el := rv.Elem()
	for el.Kind() == reflect.Ptr {
		if el.IsNil() {
			el.Set(reflect.New(el.Type().Elem()))
		}
		el = el.Elem()
	}
	if el.Kind() != reflect.Struct {
		return xerr.NewfC(xerr.CodeInvalidParam, "xreflect: MapToStruct needs a struct pointer, got %s", el.Type())
	}

	var errs []error
	for _, f := range info.Fields {
		key := f.Name
		if !cfg.useGoName && cfg.tag != "" && f.Tag != "" {
			// 与 StructToMap 对称：tag 为 "-" 表示不参与映射
			if strings.Split(f.Tag.Get(cfg.tag), ",")[0] == "-" {
				continue
			}
			key = f.MapKey(cfg.tag)
		}
		val, ok := m[key]
		if !ok {
			if val, ok = m[f.Name]; !ok {
				continue
			}
		}
		dv, ok := fieldByIndexForWrite(el, f.Index)
		if !ok || !dv.CanSet() {
			continue
		}
		if err := assignValue(dv, reflect.ValueOf(val)); err != nil {
			errs = append(errs, fieldErr(f.Name, err))
		}
	}
	if len(errs) > 0 {
		return xerr.NewMulti(errs...)
	}
	return nil
}
