# BUILD.md — 构建流程与工具链清单

> **第 4 轮更新**：Windows 原生工具链就绪（go1.26.2），G-3/G-4 门禁已在本机真实执行。
> 下述「两阶段」流程保留为历史约束，当前以「本机构建」为准。

## 一、构建流程（本机，Windows 11 / go1.26.2）

```bash
# 0. 约束（不可变）
export GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0

# 1. 门禁：vet + 全量测试（-race 需 CGO=1，测试后恢复）
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...

# 2. 产物：Windows exe + Linux 交叉编译（均静态、零第三方依赖）
go build -trimpath -ldflags="-s -w" -o bin/downloader.exe ./cmd/downloader
go build -trimpath -ldflags="-s -w" -o bin/testserver.exe  ./cmd/testserver
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/downloader_linux ./cmd/downloader

# 3. 产物验证（真实输出见 TEST_REPORT.md §3）
file bin/downloader.exe     # → PE32+ executable for MS Windows 6.01 (console), x86-64
file bin/downloader_linux   # → ELF 64-bit LSB executable, x86-64, statically linked

# 4. 端到端运行时验证（进程级，含中断续传）
bash e2e/run_e2e.sh         # 全量下载 + sha256 比对 + CLI 冒烟
bash e2e/run_resume.sh      # 限速服务端 + kill -9 + 续传 + sha256 比对
```

## 二、历史约束（两阶段：阶段 1 Linux / 阶段 2 Windows）
- 接口契约两阶段完全相同（仅 GOOS 不同）：
  `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o downloader.exe ./cmd/downloader`
- `CGO_ENABLED=0` 纯 Go 产物无动态依赖；Linux 产物 `file` 显示 statically linked。

## 三、构建约束（不可变）
- `GOFLAGS=-mod=readonly` + `GOPROXY=off`：业务依赖零联网（B-1.3）
- `CGO_ENABLED=0`：纯静态，无 libc 动态依赖（`-race` 测试时临时置 1）
- `-ldflags="-s -w"` + `-trimpath`：裁剪符号表与路径
- `go.mod`：仅 `module downloader` + Go 标准库（零第三方）

## 四、工具链清单

| 名称 | 版本 | 来源 | 备注 |
|---|---|---|---|
| Go (Windows) | go1.26.2 windows/amd64 | 本机预装（C:\Program Files\Go） | 第 4 轮起可用，门禁全部真实执行 |
| MinGW-w64 gcc | 13.2.0 (UCRT) | 本机预装 | 仅 `-race` 测试需要（CGO=1） |
| Go (Linux 交叉) | 同上（GOOS=linux） | 本机交叉编译 | 产物 `bin/downloader_linux` 静态 ELF |

> 历史（第 1~3 轮）：沙盒无 Go（`probe_result.txt`：go NOT FOUND，全部 Go 来源 403）→
> 按当时契约降级 A。第 4 轮环境迁移后门禁补齐，裁决升级 B。

## 五、打包（D8）
```bash
zip -r deliverable.zip \
  bin/downloader.exe bin/downloader_linux \
  *.md \
  cmd/ cli/ scheduler/ network/ io/ persist/ hash/ retry/ testserver/ e2e/
```

## 六、依赖完整性校验
```bash
go list -m all   # 应仅显示当前 module（无第三方）
go vet ./...     # exit=0
```
