# gokit Makefile —— 使用 /opt/go_1.21/go/bin/go
#
# 常用：
#   make check        一键质量门禁（fmt-check + vet + race 测试 + 覆盖率）
#   make cover        覆盖率报告
#   make fuzz-all     全量模糊测试回归
#   make cross-build  跨平台编译校验

GO      ?= /opt/go_1.21/go/bin/go
export GOTOOLCHAIN = local
export GOFLAGS     = -mod=mod

GOFMT   ?= $(shell dirname $(GO))/gofmt
PKGS    := ./...
FUZZTIME ?= 15s
FUZZTARGETS := \
	xconv:FuzzToInt \
	xconv:FuzzIntToBaseRoundTrip \
	xconv:FuzzValueChain \
	xslice:FuzzDedup \
	xslice:FuzzSetOperations \
	xslice:FuzzRotateAndWindow \
	xcontainer/xstack:FuzzStackLIFO \
	xcontainer/xstack:FuzzBoundedStackConservation \
	xcontainer/xqueue:FuzzSliceQueueFIFO \
	xcontainer/xqueue:FuzzRingCapacityAndFIFO \
	xcontainer/xqueue:FuzzBoundedDropPolicies \
	xlock:FuzzLockCtxNoLeak \
	xlock:FuzzLockAllKeys \
	xlock:FuzzShardOf \
	xdistlock:FuzzLockLifecycleNoLeak \
	xdistlock:FuzzTryLockEitherOr \
	xdistlock/memory:FuzzMemoryTokenSafety \
	xkeyed:FuzzShardOf \
	xkeyed:FuzzKeyedSerialOrder \
	xevent:FuzzMatchNoPanic \
	xevent:FuzzValidatePatternConsistency \
	xpipe:FuzzPipelineMatchesModel \
	xpipe:FuzzWindowMatchesModel \
	xpipe:FuzzParallelOrder_OrderAndConservation \
	xpipe:FuzzBatchConservation

.DEFAULT_GOAL := help
.PHONY: help build vet fmt fmt-check lint test test-short race cover cover-html \
        cover-check bench cross-build fuzz fuzz-all check ci clean tools

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## 编译全部包
	$(GO) build $(PKGS)

vet: ## go vet（含测试代码）
	$(GO) vet $(PKGS)

fmt: ## 格式化
	$(GOFMT) -l -w .

fmt-check: ## 检查格式（CI 用，未格式化则非零退出）
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "未格式化文件:"; echo "$$out"; exit 1; fi

lint: fmt-check vet ## 静态检查（gofmt + go vet）

test: ## 全量测试（含 -race）
	$(GO) test -race -count=1 $(PKGS)

test-short: ## 快速测试（跳过 -race，-short 模式）
	$(GO) test -short -count=1 $(PKGS)

race: test ## 别名

cover: ## 覆盖率报告（打印总分）
	$(GO) test -race -count=1 -coverprofile=cover.out -covermode=atomic $(PKGS)
	$(GO) tool cover -func=cover.out | tail -1

cover-html: cover ## 生成 HTML 覆盖率报告并打开提示
	$(GO) tool cover -html=cover.out -o cover.html
	@echo "已生成 cover.html"

cover-check: ## 覆盖率门禁（总 85% / 单包 75%，可用 TOTAL_MIN / PKG_MIN 覆盖）
	@GO=$(GO) TOTAL_MIN=$${TOTAL_MIN:-85} PKG_MIN=$${PKG_MIN:-75} ./scripts/cover-gate.sh

bench: ## 基准测试
	$(GO) test -bench=. -benchmem -run=^$$ $(PKGS)

cross-build: ## 跨平台编译校验（linux/darwin/windows）
	GOOS=linux   GOARCH=amd64 $(GO) build $(PKGS)
	GOOS=linux   GOARCH=arm64 $(GO) build $(PKGS)
	GOOS=darwin  GOARCH=arm64 $(GO) build $(PKGS)
	GOOS=windows GOARCH=amd64 $(GO) build $(PKGS)
	@echo "跨平台编译校验通过"

fuzz: ## 单目标模糊测试（用法：make fuzz T=xconv:FuzzToInt TIME=30s）
	$(GO) test -run=^$$ -fuzz=^$(word 2,$(subst :, ,$(T)))$$ -fuzztime=$(or $(TIME),$(FUZZTIME)) ./$(word 1,$(subst :, ,$(T)))

fuzz-all: ## 全量模糊测试回归（每个目标 $(FUZZTIME)）
	@fail=0; \
	for t in $(FUZZTARGETS); do \
	  pkg=$${t%%:*}; name=$${t##*:}; \
	  echo "==> fuzz $$pkg $$name"; \
	  $(GO) test -run=^$$ -fuzz=^$$name$$ -fuzztime=$(FUZZTIME) ./$$pkg > /tmp/gokit_fuzz_$$name.log 2>&1 \
	    || { echo "!! $$name 失败，日志：/tmp/gokit_fuzz_$$name.log"; fail=1; }; \
	  grep -E 'elapsed|execs' /tmp/gokit_fuzz_$$name.log | tail -1 | sed 's/^/    /' || true; \
	done; \
	if [ $$fail -ne 0 ]; then echo "!! 存在失败的 fuzz 目标"; exit 1; fi; \
	echo "==> 全量 fuzz 通过"

check: fmt-check vet test cover-check ## 一键质量门禁（推荐）
	@echo "==> check 通过"

ci: check cross-build fuzz-all ## CI 完整流水线

tools: ## 安装可选的静态检查工具（golangci-lint / staticcheck）
	@echo "golangci-lint: https://golangci-lint.run/welcome/install/"
	@echo "本仓库不强制依赖第三方 lint 工具，.golangci.yml 仅供可选使用。"

clean: ## 清理产物
	rm -f cover.out cover.html
	$(GO) clean -testcache
