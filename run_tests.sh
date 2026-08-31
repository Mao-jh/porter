#!/bin/bash
# 测试执行脚本（第 4 轮）：门禁 + 产物 + 端到端，全部如实记录原始输出
# 用法：./run_tests.sh          # 完整门禁
#       ./run_tests.sh quick    # 仅 vet + 单测
set +e
cd "$(dirname "$0")"
export GOFLAGS=-mod=readonly GOPROXY=off
: > test_raw.log

log() { echo "$@" | tee -a test_raw.log; }

log "########## [T0] 环境 ##########"
go version 2>&1 | tee -a test_raw.log

log "########## [T1] go vet ##########"
CGO_ENABLED=0 go vet ./... 2>&1 | tee -a test_raw.log
log "vet_exit=${PIPESTATUS[0]}"

log "########## [T2] 单元测试（-count=1） ##########"
CGO_ENABLED=0 go test -count=1 ./... 2>&1 | tee -a test_raw.log
log "test_exit=${PIPESTATUS[0]}"

if [ "$1" = "quick" ]; then
  log "########## quick 模式结束 ##########"
  exit 0
fi

log "########## [T3] 竞态测试（-race，需 CGO=1） ##########"
CGO_ENABLED=1 go test -race -count=1 ./... 2>&1 | tee -a test_raw.log
log "race_exit=${PIPESTATUS[0]}"

log "########## [T4] 产物构建 ##########"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/porter.exe ./cmd/porter 2>&1 | tee -a test_raw.log
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/testserver.exe ./cmd/testserver 2>&1 | tee -a test_raw.log
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/porter_linux ./cmd/porter 2>&1 | tee -a test_raw.log
ls -lh bin/ 2>&1 | tee -a test_raw.log
file bin/porter.exe bin/testserver.exe bin/porter_linux 2>&1 | tee -a test_raw.log

log "########## [T4b] TUI 模块（独立 module） ##########"
(cd tui && CGO_ENABLED=0 go vet ./... 2>&1 | tee -a ../test_raw.log; log "tui_vet_exit=${PIPESTATUS[0]}"
 CGO_ENABLED=1 go test -race -count=1 ./... 2>&1 | tee -a ../test_raw.log; log "tui_race_exit=${PIPESTATUS[0]}"
 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o porter-tui.exe ./cmd/porter-tui 2>&1 | tee -a ../test_raw.log
 log "tui_build_exit=${PIPESTATUS[0]}")

log "########## [T4c] MCP 模块（独立 module） ##########"
(cd mcp && CGO_ENABLED=0 go vet ./... 2>&1 | tee -a ../test_raw.log; log "mcp_vet_exit=${PIPESTATUS[0]}"
 CGO_ENABLED=1 go test -race -count=1 ./... 2>&1 | tee -a ../test_raw.log; log "mcp_race_exit=${PIPESTATUS[0]}"
 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o porter-mcp.exe ./cmd/porter-mcp 2>&1 | tee -a ../test_raw.log
 log "mcp_build_exit=${PIPESTATUS[0]}")

log "########## [T5] 端到端（进程级） ##########"
bash e2e/run_e2e.sh 2>&1 | tee -a test_raw.log
log "e2e_exit=${PIPESTATUS[0]}"
bash e2e/run_resume.sh 2>&1 | tee -a test_raw.log
log "resume_exit=${PIPESTATUS[0]}"
bash e2e/run_multi.sh 2>&1 | tee -a test_raw.log
log "multi_exit=${PIPESTATUS[0]}"
bash e2e/run_ftp.sh 2>&1 | tee -a test_raw.log
log "ftp_exit=${PIPESTATUS[0]}"
bash e2e/run_protocol.sh 2>&1 | tee -a test_raw.log
log "protocol_exit=${PIPESTATUS[0]}"
bash e2e/run_tui_selftest.sh 2>&1 | tee -a test_raw.log
log "tui_exit=${PIPESTATUS[0]}"
bash scripts/demo.sh $((54321 + RANDOM % 500)) 2>&1 | tee -a test_raw.log
log "demo_exit=${PIPESTATUS[0]}"

log "########## [T5b] 链接发现/抗劣化/后处理 + 修复点定向复测（第 23/24 轮） ##########"
bash scripts/run_discover_media.sh $((54323 + RANDOM % 400)) 2>&1 | tee -a test_raw.log
log "discover_media_exit=${PIPESTATUS[0]}"
bash scripts/retest_fixes.sh 2>&1 | tee -a test_raw.log
log "retest_exit=${PIPESTATUS[0]}"

log "########## [T6] 依赖完整性 ##########"
go list -m all 2>&1 | tee -a test_raw.log

log "########## [T7] 开源合规检查（第 13 轮） ##########"
bash scripts/compliance.sh 2>&1 | tee -a test_raw.log
log "compliance_exit=${PIPESTATUS[0]}"

log "########## DONE（原始输出见 test_raw.log） ##########"
