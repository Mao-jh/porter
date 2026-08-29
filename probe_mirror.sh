#!/bin/bash
# 探测腾讯镜像 Go 二进制正确路径（受限窗口内的工具链导入）
set +e
cd /data/workspace
echo "=== 探测 tencent 镜像 Go 目录 ==="
curl -sS "https://mirrors.tencent.com/golang/" 2>&1 | head -40
echo "=== 尝试 go.dev 官方直链（确认是 403 还是可达）==="
for u in "https://go.dev/dl/go1.22.5.linux-amd64.tar.gz" "https://go.dev/dl/?mode=json"; do
  echo "-- $u"
  curl -sS -o /dev/null -w "http=%{http_code} size=%{size_download}\n" "$u" 2>&1
done
echo "=== 备选：阿里云 golang 镜像路径 ==="
curl -sS "https://mirrors.aliyun.com/golang/" -o /dev/null -w "http=%{http_code}\n" 2>&1
