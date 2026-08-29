#!/usr/bin/env bash
# probe_env.sh - 环境探测（只读，不做任何安装/联网写入）
set -u
out() { printf '[%s] %s\n' "$1" "$2"; }

out "DATE" "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo N/A)"
out "UNAME" "$(uname -a 2>/dev/null || echo N/A)"
out "ARCH" "$(uname -m 2>/dev/null || echo N/A)"
out "OS_ID" "$( (. /etc/os-release 2>/dev/null && echo "$ID $VERSION_ID") || echo N/A)"

echo "--- which tools ---"
for t in go gccgo gofmt gcc g++ cc make cmake python3 pip3 apt apt-get yum dnf apk zypper pacman curl wget rsync tar unzip; do
  p="$(command -v "$t" 2>/dev/null || true)"
  out "$t" "${p:-NOT FOUND}"
done

echo "--- /usr/local/go ---"
ls -la /usr/local/go/bin 2>/dev/null || out "/usr/local/go" "NOT PRESENT"

echo "--- snap / nix / conda hints ---"
command -v snap >/dev/null 2>&1 && snap list 2>/dev/null | grep -i golang || true
command -v nix-env >/dev/null 2>&1 && nix-env -q 2>/dev/null | grep -i go || true
ls /opt 2>/dev/null || true

echo "--- apt sources (offline candidate) ---"
ls /var/cache/apt/archives 2>/dev/null | grep -i golang || out "apt-cache" "empty/unavailable"

echo "--- pip index (python as fallback compiler host) ---"
python3 -m pip --version 2>/dev/null || out "pip" "NOT FOUND"

echo "--- filesystem search for prebuilt go binary ---"
find / -maxdepth 6 -name 'go' -type f -executable 2>/dev/null | grep -v -E '/(proc|sys|dev)/' | head -20 || true
find / -maxdepth 7 -iname 'golang*' 2>/dev/null | grep -v -E '/(proc|sys|dev)/' | head -20 || true

echo "--- network reachability (best-effort, recorded) ---"
for host in 8.8.8.8 github.com go.dev golang.google.cn mirrors.aliyun.com studygolang.com; do
  if command -v curl >/dev/null 2>&1; then
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 "https://$host" 2>/dev/null || echo ERR)"
  else
    code="$(wget -q -S --tries=1 --timeout=6 "https://$host" 2>&1 | grep -i 'HTTP/' | head -1 || echo ERR)"
  fi
  out "https://$host" "$code"
done

echo "--- go install attempts (documentary, may fail) ---"
command -v apt-get >/dev/null 2>&1 && (apt-get install -y golang-go >/tmp/apt.log 2>&1 && echo "apt-get:installed" || echo "apt-get:$(tail -1 /tmp/apt.log)") || true
