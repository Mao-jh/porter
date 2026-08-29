module github.com/nymjin22/downloader

go 1.22

// 按 B-1 / H-4：零第三方运行时依赖。标准库覆盖全部需求。
// 构建约束：GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0
