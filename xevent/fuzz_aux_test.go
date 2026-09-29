package xevent_test

import "regexp"

// compileLike 用标准库正则编译表达式，作为 xevent.Match 的对照实现。
func compileLike(expr string) (*regexp.Regexp, error) { return regexp.Compile(expr) }
