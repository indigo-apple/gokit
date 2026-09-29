package xcontext_test

import (
	"context"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xcontext"
)

// 包级键：模拟真实用法。
var (
	keyInt    = xcontext.NewKey[int]("test_int")
	keyInt64  = xcontext.NewKey[int64]("test_int64")
	keyStr    = xcontext.NewKey[string]("test_str")
	keyStruct = xcontext.NewKey[testPayload]("test_struct")
)

type testPayload struct {
	Name string
	N    int
}

// ---------------------------------------------------------------------------
// 基础存取
// ---------------------------------------------------------------------------

func TestWithGet(t *testing.T) {
	ctx := context.Background()
	ctx = xcontext.With(ctx, keyInt, 7)
	ctx = xcontext.With(ctx, keyInt64, int64(8))
	ctx = xcontext.With(ctx, keyStr, "s")
	ctx = xcontext.With(ctx, keyStruct, testPayload{Name: "n", N: 1})

	if v, ok := xcontext.Get(ctx, keyInt); !ok || v != 7 {
		t.Fatalf("int = %v,%v", v, ok)
	}
	if v, ok := xcontext.Get(ctx, keyInt64); !ok || v != 8 {
		t.Fatalf("int64 = %v,%v", v, ok)
	}
	if v, ok := xcontext.Get(ctx, keyStr); !ok || v != "s" {
		t.Fatalf("string = %v,%v", v, ok)
	}
	if v, ok := xcontext.Get(ctx, keyStruct); !ok || v.Name != "n" || v.N != 1 {
		t.Fatalf("struct = %v,%v", v, ok)
	}
}

func TestGetMissing(t *testing.T) {
	ctx := context.Background()
	if v, ok := xcontext.Get(ctx, keyInt); ok || v != 0 {
		t.Fatalf("missing int = %v,%v", v, ok)
	}
	if v, ok := xcontext.Get(ctx, keyStr); ok || v != "" {
		t.Fatalf("missing string = %v,%v", v, ok)
	}
	if v, ok := xcontext.Get(ctx, keyStruct); ok || v.Name != "" {
		t.Fatalf("missing struct = %v,%v", v, ok)
	}
	// nil 安全
	if _, ok := xcontext.Get(nil, keyInt); ok {
		t.Fatal("nil ctx should be false")
	}
	if _, ok := xcontext.Get[int](ctx, nil); ok {
		t.Fatal("nil key should be false")
	}
	if got := xcontext.Or(nil, keyInt, 99); got != 99 {
		t.Fatalf("Or(nil) = %v", got)
	}
	if got := xcontext.Or(ctx, nil, 99); got != 99 {
		t.Fatalf("Or(nil key) = %v", got)
	}
}

func TestDistinctKeysDoNotCollide(t *testing.T) {
	// 同名但类型不同的键互不影响
	otherInt := xcontext.NewKey[int]("test_int")
	ctx := xcontext.With(context.Background(), keyInt, 1)
	if _, ok := xcontext.Get(ctx, otherInt); ok {
		t.Fatal("different key instances must not collide")
	}
}

func TestWithNilCtxAndKey(t *testing.T) {
	ctx := xcontext.With(nil, keyInt, 5)
	if v, ok := xcontext.Get(ctx, keyInt); !ok || v != 5 {
		t.Fatalf("With(nil ctx) = %v,%v", v, ok)
	}
	if got := xcontext.With(nil, nil, 1); got == nil {
		t.Fatal("With(nil,nil) should return Background")
	}
	base := context.Background()
	if got := xcontext.With(base, nil, 1); got != base {
		t.Fatal("With(base, nil key) should return base unchanged")
	}
}

func TestWithOverride(t *testing.T) {
	ctx := xcontext.With(context.Background(), keyStr, "first")
	ctx = xcontext.With(ctx, keyStr, "second")
	if v, _ := xcontext.Get(ctx, keyStr); v != "second" {
		t.Fatalf("got %q, want second", v)
	}
}

func TestExplicitZeroValueIsFound(t *testing.T) {
	ctx := xcontext.With(context.Background(), keyStr, "")
	v, ok := xcontext.Get(ctx, keyStr)
	if !ok || v != "" {
		t.Fatalf("explicit empty string should be found: %v,%v", v, ok)
	}
	if got := xcontext.Or(ctx, keyStr, "default"); got != "" {
		t.Fatalf("Or should return the stored empty string, got %q", got)
	}
}

func TestOr(t *testing.T) {
	ctx := xcontext.With(context.Background(), keyInt, 1)
	if got := xcontext.Or(ctx, keyInt, 2); got != 1 {
		t.Fatalf("Or(present) = %d", got)
	}
	if got := xcontext.Or(context.Background(), keyInt, 2); got != 2 {
		t.Fatalf("Or(absent) = %d", got)
	}
}

func TestMust(t *testing.T) {
	ctx := xcontext.With(context.Background(), keyInt, 42)
	if got := xcontext.Must(ctx, keyInt); got != 42 {
		t.Fatalf("Must = %d", got)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Must should panic when key is missing")
		}
		if !strings.Contains(toString(r), "test_int") {
			t.Fatalf("panic message should mention key name, got %v", r)
		}
	}()
	xcontext.Must(context.Background(), keyInt)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ---------------------------------------------------------------------------
// 键注册表
// ---------------------------------------------------------------------------

func TestKeysAndRegistered(t *testing.T) {
	keys := xcontext.Keys()
	if len(keys) == 0 {
		t.Fatal("Keys should not be empty")
	}
	if !xcontext.Registered("test_int") {
		t.Fatal("test_int should be registered")
	}
	if xcontext.Registered("definitely_not_registered_key") {
		t.Fatal("unknown key should not be registered")
	}
	// 内置键必须已注册
	for _, name := range []string{"trace_id", "span_id", "user_id", "tenant_id", "request_id", "locale"} {
		if !xcontext.Registered(name) {
			t.Fatalf("builtin key %q not registered", name)
		}
	}
}

func TestValuesAndLen(t *testing.T) {
	if got := xcontext.Values(nil); got != nil {
		t.Fatalf("Values(nil) = %#v", got)
	}
	if got := xcontext.Len(nil); got != 0 {
		t.Fatalf("Len(nil) = %d", got)
	}

	// 空 context：没有任何已注册键被设置
	empty := context.Background()
	if got := xcontext.Values(empty); got != nil {
		t.Fatalf("Values(empty) = %#v, want nil", got)
	}
	if got := xcontext.Len(empty); got != 0 {
		t.Fatalf("Len(empty) = %d", got)
	}

	ctx := xcontext.With(context.Background(), keyInt, 1)
	ctx = xcontext.WithTraceID(ctx, "t-1")
	v := xcontext.Values(ctx)
	if v["test_int"] != 1 || v["trace_id"] != "t-1" {
		t.Fatalf("Values = %#v", v)
	}
	if len(v) != 2 {
		t.Fatalf("Values len = %d, want 2 (%#v)", len(v), v)
	}
	if got := xcontext.Len(ctx); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// 内置键便捷函数
// ---------------------------------------------------------------------------

func TestBuiltinHelpers(t *testing.T) {
	ctx := context.Background()

	ctx = xcontext.WithTraceID(ctx, "tr")
	ctx = xcontext.WithSpanID(ctx, "sp")
	ctx = xcontext.WithUserID(ctx, "u1")
	ctx = xcontext.WithTenantID(ctx, "t1")
	ctx = xcontext.WithRequestID(ctx, "r1")
	ctx = xcontext.WithLocale(ctx, "zh-CN")

	cases := []struct {
		got, want string
		name      string
	}{
		{xcontext.TraceID(ctx), "tr", "trace"},
		{xcontext.SpanID(ctx), "sp", "span"},
		{xcontext.UserID(ctx), "u1", "user"},
		{xcontext.TenantID(ctx), "t1", "tenant"},
		{xcontext.RequestID(ctx), "r1", "request"},
		{xcontext.Locale(ctx), "zh-CN", "locale"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	// 空 context 返回空串而非 panic
	empty := context.Background()
	if xcontext.TraceID(empty) != "" || xcontext.UserID(empty) != "" ||
		xcontext.TenantID(empty) != "" || xcontext.RequestID(empty) != "" ||
		xcontext.Locale(empty) != "" || xcontext.SpanID(empty) != "" {
		t.Fatal("empty context should yield empty strings")
	}
	if xcontext.TraceID(nil) != "" {
		t.Fatal("nil context should yield empty string")
	}
}

// ---------------------------------------------------------------------------
// Detach / Copy
// ---------------------------------------------------------------------------

func TestDetach(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	ctx := xcontext.With(base, keyStr, "v")
	ctx = xcontext.WithTraceID(ctx, "tr")

	det := xcontext.Detach(ctx)
	if got := xcontext.Or(det, keyStr, ""); got != "v" {
		t.Fatalf("Detach should keep values, got %q", got)
	}
	if xcontext.TraceID(det) != "tr" {
		t.Fatal("Detach should keep trace id")
	}
	if det.Done() != nil {
		t.Fatal("Detach should drop the Done channel")
	}
	if _, ok := det.Deadline(); ok {
		t.Fatal("Detach should drop the deadline")
	}

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("original ctx should be canceled")
	}
	select {
	case <-det.Done():
		t.Fatal("detached ctx must not be canceled")
	default:
	}

	if got := xcontext.Detach(nil); got == nil || got.Done() != nil {
		t.Fatal("Detach(nil) should return a background-like context")
	}
}

func TestCopy(t *testing.T) {
	ctx := xcontext.With(context.Background(), keyStr, "v")
	cp := xcontext.Copy(ctx)
	if v, ok := xcontext.Get(cp, keyStr); !ok || v != "v" {
		t.Fatalf("Copy lost value: %v,%v", v, ok)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMergeValuesOverridePriority(t *testing.T) {
	base := xcontext.With(context.Background(), keyStr, "base")
	base = xcontext.With(base, keyInt, 1)

	over := xcontext.With(context.Background(), keyStr, "over")

	m := xcontext.Merge(base, over)
	if v, _ := xcontext.Get(m, keyStr); v != "over" {
		t.Fatalf("override should win, got %q", v)
	}
	if v, _ := xcontext.Get(m, keyInt); v != 1 {
		t.Fatalf("base-only key lost, got %d", v)
	}
}

func TestMergeNilArguments(t *testing.T) {
	ctx := context.Background()
	if got := xcontext.Merge(nil, ctx); got != ctx {
		t.Fatal("Merge(nil, x) should return x")
	}
	if got := xcontext.Merge(ctx, nil); got != ctx {
		t.Fatal("Merge(x, nil) should return x")
	}
	if got := xcontext.Merge(nil, nil); got != nil {
		t.Fatal("Merge(nil,nil) should return nil")
	}
}

func TestMergeNoCancelSource(t *testing.T) {
	m := xcontext.Merge(context.Background(), context.Background())
	if m.Done() == nil {
		t.Fatal("Done should not be nil")
	}
	if err := m.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if _, ok := m.Deadline(); ok {
		t.Fatal("no deadline expected")
	}
	select {
	case <-m.Done():
		t.Fatal("merged should not be canceled")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestMergeBaseCancel(t *testing.T) {
	base, bcancel := context.WithCancel(context.Background())
	over, ocancel := context.WithCancel(context.Background())
	defer ocancel()

	m := xcontext.Merge(base, over)
	bcancel()

	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("merged should be canceled when base is canceled")
	}
	if m.Err() != context.Canceled {
		t.Fatalf("Err = %v, want context.Canceled", m.Err())
	}
}

func TestMergeOverrideCancel(t *testing.T) {
	base, bcancel := context.WithCancel(context.Background())
	defer bcancel()
	over, ocancel := context.WithCancel(context.Background())

	m := xcontext.Merge(base, over)
	ocancel()

	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("merged should be canceled when override is canceled")
	}
}

func TestMergeDeadlineIsEarliest(t *testing.T) {
	base, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer bcancel()
	over, ocancel := context.WithTimeout(context.Background(), time.Hour)
	defer ocancel()

	m := xcontext.Merge(base, over)
	got, ok := m.Deadline()
	if !ok {
		t.Fatal("deadline expected")
	}
	want, _ := base.Deadline()
	if !got.Equal(want) {
		t.Fatalf("deadline = %v, want earliest %v", got, want)
	}

	// 反过来也应取更早的
	base2, b2 := context.WithTimeout(context.Background(), time.Hour)
	defer b2()
	over2, o2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer o2()
	m2 := xcontext.Merge(base2, over2)
	got2, ok2 := m2.Deadline()
	if !ok2 {
		t.Fatal("deadline expected")
	}
	want2, _ := over2.Deadline()
	if !got2.Equal(want2) {
		t.Fatalf("deadline = %v, want %v", got2, want2)
	}
}

func TestMergeEitherAlreadyCanceled(t *testing.T) {
	base, bcancel := context.WithCancel(context.Background())
	bcancel()
	over, ocancel := context.WithCancel(context.Background())
	defer ocancel()

	m := xcontext.Merge(base, over)
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("merged should be canceled immediately when base is already canceled")
	}
	if m.Err() != context.Canceled {
		t.Fatalf("Err = %v", m.Err())
	}

	base2, b2 := context.WithCancel(context.Background())
	defer b2()
	over2, o2 := context.WithCancel(context.Background())
	o2()
	m2 := xcontext.Merge(base2, over2)
	select {
	case <-m2.Done():
	case <-time.After(time.Second):
		t.Fatal("merged should be canceled when override is already canceled")
	}
}

func TestMergeDeadlineExpiry(t *testing.T) {
	base, bcancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer bcancel()
	defer xcontext.Detach(base)

	m := xcontext.Merge(base, context.Background())
	select {
	case <-m.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("merged should be canceled after base deadline")
	}
	if m.Err() != context.DeadlineExceeded {
		t.Fatalf("Err = %v, want DeadlineExceeded", m.Err())
	}
}

func TestMergeCancel(t *testing.T) {
	base, bcancel := context.WithCancel(context.Background())
	defer bcancel()
	over, ocancel := context.WithCancel(context.Background())
	defer ocancel()

	m, cancel := xcontext.MergeCancel(base, over)
	cancel()
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("MergeCancel cancel should cancel the merged context")
	}
	// 重复调用 cancel 安全
	cancel()
}

func TestMergeNoGoroutineLeak(t *testing.T) {
	// 预热，避免首次运行的一次性 goroutine 干扰计数
	for i := 0; i < 5; i++ {
		b, bc := context.WithCancel(context.Background())
		o, oc := context.WithCancel(context.Background())
		m := xcontext.Merge(b, o)
		bc()
		oc()
		<-m.Done()
	}
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		b, bc := context.WithCancel(context.Background())
		o, oc := context.WithCancel(context.Background())
		m := xcontext.Merge(b, o)
		bc()
		oc()
		<-m.Done()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+10 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("疑似 goroutine 泄漏: before=%d after=%d", before, runtime.NumGoroutine())
}

// ---------------------------------------------------------------------------
// HTTP 集成
// ---------------------------------------------------------------------------

func TestFromRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set(xcontext.HeaderRequestID, "req-1")
	req.Header.Set(xcontext.HeaderTraceID, "trace-1")
	req.Header.Set(xcontext.HeaderTenantID, "tenant-1")
	req.Header.Set(xcontext.HeaderUserID, "user-1")
	req.Header.Set(xcontext.HeaderLocale, "zh-CN")

	ctx := xcontext.FromRequest(req)
	if xcontext.RequestID(ctx) != "req-1" ||
		xcontext.TraceID(ctx) != "trace-1" ||
		xcontext.TenantID(ctx) != "tenant-1" ||
		xcontext.UserID(ctx) != "user-1" ||
		xcontext.Locale(ctx) != "zh-CN" {
		t.Fatalf("headers not extracted: %#v", xcontext.Values(ctx))
	}
}

func TestFromRequestTraceparentFallback(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/x", nil)
	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	req.Header.Set("traceparent", "00-"+tid+"-00f067aa0ba902b7-01")

	ctx := xcontext.FromRequest(req)
	if got := xcontext.TraceID(ctx); got != tid {
		t.Fatalf("trace id = %q, want %q", got, tid)
	}

	// X-Trace-Id 优先于 traceparent
	req.Header.Set(xcontext.HeaderTraceID, "explicit")
	if got := xcontext.TraceID(xcontext.FromRequest(req)); got != "explicit" {
		t.Fatalf("explicit header should win, got %q", got)
	}
}

func TestFromRequestAcceptLanguageFallback(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if got := xcontext.Locale(xcontext.FromRequest(req)); got != "en-US,en;q=0.9" {
		t.Fatalf("locale = %q", got)
	}
}

func TestFromRequestEmptyAndNil(t *testing.T) {
	if got := xcontext.FromRequest(nil); got == nil {
		t.Fatal("FromRequest(nil) should not be nil")
	}
	req := httptest.NewRequest("GET", "/api/x", nil)
	ctx := xcontext.FromRequest(req)
	if v := xcontext.Values(ctx); v != nil {
		t.Fatalf("no headers should yield no values, got %#v", v)
	}
}

func TestFromRequestKeepsCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/x", nil).WithContext(base)
	cancel()
	ctx := xcontext.FromRequest(req)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("FromRequest should keep request cancellation")
	}
}

func TestFromRequestWhitespaceHeaderIgnored(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set(xcontext.HeaderTraceID, "   ")
	if got := xcontext.TraceID(xcontext.FromRequest(req)); got != "" {
		t.Fatalf("blank header should be ignored, got %q", got)
	}
}

func TestTraceparentInvalid(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"00-short-01",
		"00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
	}
	for _, tp := range cases {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("traceparent", tp)
		if got := xcontext.TraceID(xcontext.FromRequest(req)); got != "" {
			t.Fatalf("traceparent %q should be rejected, got %q", tp, got)
		}
	}
	// 合法但 span/flags 缺失也应可用
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7")
	if got := xcontext.TraceID(xcontext.FromRequest(req)); got == "" {
		t.Fatal("valid traceparent should be accepted")
	}
}
