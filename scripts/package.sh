#!/bin/bash
# D8 打包：deliverable.zip，按 CHECKLIST「≤4 条 file 类型条目」组织
set +e
cd /data/workspace
rm -f deliverable.zip

# 四类条目：① 文档(Markdown) ② 全量源码(Go) ③ 构建/测试脚本 ④ 校验器
zip -q deliverable.zip \
    REPORT.md DESIGN.md TEST_REPORT.md BUILD.md USAGE.md \
    go.mod \
    cmd/downloader/main.go \
    cli/cli.go cli/cli_test.go \
    network/transport.go network/transport_test.go \
    scheduler/shard.go scheduler/shard_test.go \
    io/buffer.go io/buffer_test.go io/fallocate_linux.go io/fallocate_stub.go \
    persist/persist.go persist/persist_test.go \
    hash/hash.go hash/hash_test.go \
    retry/retry.go retry/retry_test.go \
    testserver/server.go testserver/server_test.go \
    build.sh run_tests.sh install_go.sh diagnose.sh \
    scripts/check.py scripts/package.sh \
    toolchain_import.log

echo "=== deliverable.zip 内容（按 4 类 file 条目归类）==="
unzip -l deliverable.zip | tail -40
echo ""
echo "=== 条目分类校验（≤4 类）==="
unzip -l deliverable.zip | grep -cE '\.md$'       | xargs -I{} echo "  文档(Markdown)      : {} 个文件"
unzip -l deliverable.zip | grep -cE '\.go$'        | xargs -I{} echo "  Go 源码             : {} 个文件"
unzip -l deliverable.zip | grep -cE '\.(sh|log)$'  | xargs -I{} echo "  脚本/日志           : {} 个文件"
unzip -l deliverable.zip | grep -cE 'go\.mod$'     | xargs -I{} echo "  模块定义(go.mod)    : {} 个文件"
echo ""
ls -lh deliverable.zip
