#!/usr/bin/env bash
# run_discover_media.sh — 链接发现 / 抗劣化下载 / 下载后处理 一键实测（第 23 轮）。
# 用法：./scripts/run_discover_media.sh [端口]（默认 54323；需 bin/ 已有 porter.exe/testserver.exe）
# 退出码 0 = 全部通过；非 0 = 有失败项（逐项打印）。
set -u
PORT="${1:-54323}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
P="$ROOT/bin/porter.exe"
TS="$ROOT/bin/testserver.exe"
WORK=""  # 在测试段赋值（PID 隔离目录）
PASS=0; FAIL=0

ok()   { PASS=$((PASS+1)); echo "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL: $1"; }

check() { # check <描述> <命令...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}

echo "== 准备测试环境 =="
# 固定 cwd 到项目根：Git Bash 的 /c/... 绝对路径传给 Windows 原生程序时
# 不做路径转换（open 会失败），统一用相对路径
cd "$ROOT"
# 每次运行用独立工作目录（PID 隔离），避免与历史残留冲突，无需先清理
WORK="e2e/dmdata.$$"
mkdir -p "$WORK"
# 启动 HTTP（含 /page/ 路由）+ FTP 测试服务端
"$TS" -dir "$WORK" -addr "127.0.0.1:$PORT" -name big.bin -size 8388608 \
  -extra "tiny.bin:1048576,ep1.mp4:2097152,ep2.mkv:3145728" -ftp \
  > "$WORK/server.log" 2>&1 &
TSRV=$!
trap 'kill $TSRV ${TSRV2:-} 2>/dev/null' EXIT
sleep 1.5
BASE="http://127.0.0.1:$PORT"
FTP=$(grep '^ftpurl=' "$WORK/server.log" | cut -d= -f2 | sed 's#/[^/]*$#/#')  # 目录形式

echo "== 1. 链接发现 =="
N_FIND=$("$P" find "$BASE/page/" 2>/dev/null | wc -l)
[ "$N_FIND" -ge 5 ] && ok "find 提取页面链接（$N_FIND 个）" || bad "find 提取页面链接（$N_FIND 个）"
N_FILT=$("$P" find "$BASE/page/" -ext mp4,mkv 2>/dev/null | grep -cE '\.(mp4|mkv)$')
[ "$N_FILT" -ge 1 ] && ok "find -ext 过滤（$N_FILT 个 mp4/mkv）" || bad "find -ext 过滤"
N_DEPTH=$("$P" find "$BASE/page/" -depth 2 2>/dev/null | wc -l)
[ "$N_DEPTH" -ge 8 ] && ok "find -depth 2 递归到子页（$N_DEPTH 个）" || bad "find -depth 2 递归（$N_DEPTH 个）"
N_LS=$("$P" ls "$FTP" 2>/dev/null | wc -l)
[ "$N_LS" -ge 4 ] && ok "ls FTP 目录（$N_LS 个条目）" || bad "ls FTP 目录（$N_LS 个条目）"
printf '<DT><A HREF="%s/file/ep1.mp4">E1</A>\n' "$BASE" > "$WORK/bm.html"
"$P" bookmarks "$WORK/bm.html" 2>/dev/null | grep -q ep1.mp4 && ok "bookmarks 解析" || bad "bookmarks 解析"
echo "x $BASE/file/big.bin y" | "$P" extract - 2>/dev/null | grep -q big.bin && ok "extract 文本提取" || bad "extract 文本提取"
# 手工构造 .torrent（info_hash 独立验证）
python3 - "$WORK" <<'PYEOF'
import hashlib, sys
info = b'd6:lengthi1024e4:name9:movie.bin12:piece lengthi16384e6:pieces20:01234567890123456789e'
t = b'd8:announce30:http://tracker.example.com/ann4:info' + info + b'8:url-listl33:http://seed.example.com/movie.binee'
open(sys.argv[1]+'/m.torrent','wb').write(t)
open(sys.argv[1]+'/ih.txt','w').write(hashlib.sha1(info).hexdigest())
PYEOF
IH_GOT=$("$P" torrent "$WORK/m.torrent" 2>/dev/null | grep info_hash | cut -d= -f2)
IH_WANT=$(cat "$WORK/ih.txt")
[ "$IH_GOT" = "$IH_WANT" ] && ok "torrent 解析（info_hash 与独立 SHA1 一致）" || bad "torrent 解析（$IH_GOT vs $IH_WANT）"

echo "== 2. 抗劣化下载 =="
"$P" "$BASE/file/nonexist.bin" -mirror "$BASE/file/tiny.bin" -o "$WORK/m.bin" -state-dir "$WORK/st" >/dev/null 2>&1
if [ $? -eq 0 ] && cmp -s "$WORK/m.bin" "$WORK/tiny.bin"; then
  ok "镜像切换（主源 404 自动切镜像，哈希一致）"
else
  bad "镜像切换"
fi
# 慢速保护：限速源（每连接 50KB/s）min-rate 阈值高于实际 → 切不限速镜像
"$TS" -dir "$WORK" -addr "127.0.0.1:$((PORT+1))" -name big.bin -size 8388608 -limit 51200 \
  > "$WORK/slow.log" 2>&1 &
TSRV2=$!
trap 'kill $TSRV $TSRV2 2>/dev/null' EXIT
sleep 1
SLOW="http://127.0.0.1:$((PORT+1))"
"$P" "$SLOW/file/big.bin" -mirror "$BASE/file/big.bin" -min-rate 200000 -o "$WORK/mr.bin" -state-dir "$WORK/st2" > "$WORK/mr.log" 2>&1
if [ $? -eq 0 ] && grep -q mirror "$WORK/mr.log" && cmp -s "$WORK/mr.bin" "$WORK/big.bin"; then
  ok "慢速保护（30s 窗口判慢 → 切镜像，哈希一致）"
else
  bad "慢速保护"
fi
# retry-forever：不可达端口 → 指数退避持续重试（8s 截断观察重试日志）
timeout 8 "$P" "http://127.0.0.1:59999/x.bin" -retry-forever -o "$WORK/rf.bin" > "$WORK/rf.log" 2>&1
grep -q 'retry-forever' "$WORK/rf.log" && ok "retry-forever 无限重试（指数退避）" || bad "retry-forever 无限重试"

echo "== 3. 下载后处理 =="
# 构造媒体文件（真实容器头）与"下载目录"垃圾场景
python3 - "$WORK" <<'PYEOF'
import struct, sys, os
d = sys.argv[1]
def box(t, p): return struct.pack('>I4s', 8+len(p), t.encode()) + p
open(d+'/s.wav','wb').write(b'RIFF'+struct.pack('<I',36+176400)+b'WAVE'
    +b'fmt '+struct.pack('<IHHIIHH',16,1,2,44100,44100*4,4,16)
    +b'data'+struct.pack('<I',176400)+b'\x00'*176400)
mvhd = b'\x00'*12 + struct.pack('>II', 1000, 12000)
tkhd = b'\x00'*12 + struct.pack('>I',1) + b'\x00'*4 + struct.pack('>I',12000) + b'\x00'*8 \
     + struct.pack('>HHHH',0,0,0,0) + b'\x00'*36 + struct.pack('>II',1280<<16,720<<16)
stsd = struct.pack('>II',0,1) + struct.pack('>I',86) + b'avc1' + b'\x00'*70
stbl = box('stbl', box('stsd', stsd))
minf = box('minf', stbl)
mdia = box('mdia', minf)
trak = box('trak', box('tkhd', tkhd) + mdia)
moov = box('moov', box('mvhd', mvhd) + trak)
open(d+'/v.mp4','wb').write(box('ftyp',b'isom') + moov)
os.makedirs(d+'/dldir', exist_ok=True)
vid = open(d+'/v.mp4','rb').read()
open(d+'/dldir/m.mp4','wb').write(vid)
open(d+'/dldir/m2.mp4','wb').write(vid)
open(d+'/dldir/s.wav','wb').write(open(d+'/s.wav','rb').read())
open(d+'/dldir/promo.txt','w').write('ad')
open(d+'/dldir/m.mp4.url','w').write('x')
PYEOF
"$P" info "$WORK/v.mp4" 2>/dev/null | grep -q 'duration=12s' && "$P" info "$WORK/v.mp4" 2>/dev/null | grep -q '1280x720' \
  && ok "info 媒体预览（MP4 时长/分辨率/编码）" || bad "info 媒体预览（MP4）"
"$P" info "$WORK/s.wav" 2>/dev/null | grep -q 'duration=1s samplerate=44100' && ok "info WAV 时长/采样率" || bad "info WAV"
"$P" transcode "$WORK/s.wav" -to mp3 -quiet >/dev/null 2>&1 \
  && "$P" info "$WORK/s.mp3" 2>/dev/null | grep -q kind=mp3 && ok "transcode WAV→MP3（真实 ffmpeg）" || bad "transcode WAV→MP3"
# 先 scrub（清顶层垃圾）再 organize（归类）——与真实用户流程一致
"$P" scrub "$WORK/dldir" > "$WORK/scrub.log" 2>&1
grep -q '.trash' "$WORK/scrub.log" && [ -f "$WORK/dldir/.trash/promo.txt" ] && ok "scrub 广告/垃圾清理" || bad "scrub 清理"
"$P" organize "$WORK/dldir" -dedupe > "$WORK/org.log" 2>&1
grep -q dupe "$WORK/org.log" && [ -f "$WORK/dldir/audio/s.wav" ] && ok "organize 归类 + 哈希去重" || bad "organize 归类 + 去重"
mkdir -p "$WORK/dry" && echo x > "$WORK/dry/a.mp4"
"$P" organize "$WORK/dry" -dry-run 2>/dev/null | grep -q dry-run && [ -f "$WORK/dry/a.mp4" ] \
  && ok "dry-run 不移动（organize）" || bad "dry-run 不移动"

echo ""
echo "== 结果: PASS=$PASS FAIL=$FAIL =="
[ "$FAIL" -eq 0 ] && echo "ALL PASSED" || echo "HAS FAILURES"
exit $FAIL
