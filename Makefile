# gokit Makefile —— 使用 /opt/go_1.21/go/bin/go

GO      ?= /opt/go_1.21/go/bin/go
export GOTOOLCHAIN = local
export GOFLAGS     = -mod=mod

GOFMT   ?= $(shell dirname $(GO))/gofmt
PKGS    := ./...

.DEFAULT_GOAL := help
.PHONY: help build vet fmt fmt-check test test-short cover cover-check bench race cross-build check clean fuzz-short

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## 编译全部包
	$(GO) build $(PKGS)

vet: ## go vet + go build
	$(GO) vet $(PKGS)

fmt: ## 格式化
	$(GOFMT) -l -w .

fmt-check: ## 检查格式（CI 用，非零退出表示未格式化）
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "未格式化文件:"; echo "$$out"; exit 1; fi

test: ## 全量测试（含 -race）
	$(GO) test -race -count=1 $(PKGS)

test-short: ## 快速测试（跳过 -race）
	$(GO) test -count=1 $(PKGS)

race: test ## 别名

cover: ## 覆盖率报告
	$(GO) test -race -count=1 -coverprofile=cover.out -covermode=atomic $(PKGS)
	$(GO) tool cover -func=cover.out | tail -1

cover-check: ## 覆盖率门禁（默认 85%）
	@$(GO) test -count=1 -coverprofile=cover.out -covermode=atomic $(PKGS) > /dev/null
	@total=$$($(GO) tool cover -func=cover.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	 echo "总覆盖率: $$total%"; \
	 awk -v t="$$total" 'BEGIN{ if (t+0 < 85) { print "覆盖率低于 85%"; exit 1 } }'

bench: ## 基准测试
	$(GO) test -bench=. -benchmem -run=^$$ $(PKGS)

cross-build: ## 跨平台编译校验
	GOOS=linux   GOARCH=amd64 $(GO) build $(PKGS)
	GOOS=linux   GOARCH=arm64 $(GO) build $(PKGS)
	GOOS=darwin  GOARCH=arm64 $(GO) build $(PKGS)
	GOOS=windows GOARCH=amd64 $(GO) build $(PKGS)

fuzz-short: ## 短时模糊测试
	$(GO) test -run=^$$ -fuzz=FuzzToInt -fuzztime=15s ./xconv || true
	$(GO) test -run=^$$ -fuzz=FuzzDedup  -fuzztime=15s ./xslice || true

check: build vet fmt-check test ## 一键质量门禁

clean: ## 清理产物
	rm -f cover.out cover.html
	$(GO) clean -cache -testcache
