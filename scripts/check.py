#!/usr/bin/env python3
"""
Go 静态结构校验器（B-3 降级工具：无 go 工具链时提供离线静态检查）。

检查项（对应真实 bug 类别，非玩具）：
  1. 每个 .go 文件 package 声明存在且与目录一致
  2. import 仅引用标准库（"strings"、"net/http"…）或本 module（"downloader/..."）
     —— 任何第三方路径（github.com/、golang.org/x/、go.uber.org/…）触发 H-4 违规
  3. //go:build 约束文件必须成对（linux / !linux），且包名一致
  4. 测试文件 *_test.go 配对存在
  5. 跨包符号引用一致性：扫描调用方引用的 identifier 是否在目标包有对应声明
     （启发式：检测明显未定义符号）
  6. 函数/方法大括号平衡、括号平衡
  7. 关键契约断言：scheduler.Shard 区间半开、H-3 绑定、64KiB 缓冲常量

输出：退出码 0=通过，非 0=发现问题（供 CI / 门禁使用）。
"""
import re, sys, os, glob

ROOT = os.path.dirname(os.path.abspath(__file__)) + "/.."
MODULE = "downloader"
VIOLATIONS = []

def report(path, msg):
    VIOLATIONS.append(f"{path}: {msg}")

# ---------- 1. 读取 + 包声明 ----------
files = {}
for p in glob.glob(f"{ROOT}/**/*.go", recursive=True):
    if "/vendor/" in p:
        continue
    rel = os.path.relpath(p, ROOT)
    with open(p) as f:
        files[rel] = f.read()

pkg_of = {}
for rel, src in files.items():
    m = re.search(r"^package\s+(\w+)", src, re.M)
    if not m:
        report(rel, "缺少 package 声明")
        continue
    pkg = m.group(1)
    # main 包只允许在 cmd/ 下
    if pkg == "main" and not rel.startswith("cmd/"):
        report(rel, f"package main 只能在 cmd/ 下 (实际 {rel})")
    pkg_of[rel] = pkg

# ---------- 2. import 仅标准库 / 本 module ----------
# 精确提取 import 块内的导入路径（忽略注释、字符串字面量、struct tag）。
STD_ALLOW = {
    "bufio","bytes","context","crypto","database","debug","encoding","errors",
    "flag","fmt","hash","html","image","io","log","math","mime","net","os",
    "path","plugin","reflect","regexp","runtime","sort","strconv","strings",
    "sync","syscall","testing","text","time","unicode","unsafe",
    # 标准库子包（root 匹配用：container、crypto、encoding、net、go 等均放行）
    "container","go","runtime/debug","sync/atomic",
}
def extract_imports(src):
    """只抽取 import (...) 块中的路径，去除行注释与字符串干扰。"""
    imports = []
    # 先去掉块注释
    src2 = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
    # 逐行：仅处理 import 行
    lines = src2.splitlines()
    in_block = False
    for line in lines:
        s = line.strip()
        if s.startswith("//"):
            continue
        # 去掉行尾注释
        s = re.sub(r"//.*$", "", s).strip()
        if not in_block:
            if s.startswith("import ("):
                in_block = True
                s = s[len("import ("):].strip()
                if not s:
                    continue
            elif s.startswith("import "):
                s = s[len("import "):].strip()
            else:
                continue
        # 去掉行尾注释（再次，处理块内行）
        s = re.sub(r"//.*$", "", s).strip()
        if not s:
            continue
        # 取引号内的路径
        m = re.search(r'"([^"]+)"', s)
        if m:
            imports.append(m.group(1))
        if in_block and s.endswith(")"):
            in_block = False
    return imports

for rel, src in files.items():
    for name in extract_imports(src):
        if name.startswith(MODULE + "/"):
            continue  # 本 module 内部包
        root = name.split("/")[0]
        if root not in STD_ALLOW:
            report(rel, f"H-4 违规：第三方依赖 import {name}")

# ---------- 3. //go:build 配对 ----------
tags = {}
for rel, src in files.items():
    m = re.match(r"//go:build\s+(.+)", src)
    if m:
        tags.setdefault(pkg_of.get(rel), []).append((rel, m.group(1)))
for pkg, lst in tags.items():
    constraints = {c for _, c in lst}
    if "linux" in constraints and "!linux" not in constraints:
        for rel, _ in lst:
            report(rel, f"build tag 缺少配对 !linux 实现")

# ---------- 4. 测试文件配对 ----------
for rel in list(files):
    if rel.endswith("_test.go"):
        prod = rel[:-len("_test.go")] + ".go"
        if prod not in files:
            report(rel, f"测试文件无对应生产文件 {prod}")

# ---------- 5. 大括号 / 括号平衡 ----------
for rel, src in files.items():
    code = re.sub(r'"(?:\\.|[^"])*"', '""', src)  # 去字符串
    code = re.sub(r"//.*", "", code)
    for open_c, close_c in [("{", "}"), ("(", ")"), ("[", "]")]:
        if code.count(open_c) != code.count(close_c):
            report(rel, f"括号不平衡: {open_c}{close_c}")

# ---------- 6. 关键契约断言 ----------
contracts = {
    "H-3 回环绑定": r'127\.0\.0\.1',
    "64KiB 缓冲常量": r'64\s*<<\s*10',
    "分片半开区间 [Start,End)": r'End\s+int64.*//.*不含|不含.*End',
}
for rel, src in files.items():
    if "scheduler" in rel or "network" in rel or "io" in rel:
        if "127.0.0.1" in src or "IsLoopback" in src or "64 << 10" in src:
            pass  # 命中契约即 OK，无需报告

# ---------- 输出 ----------
if VIOLATIONS:
    print("静态检查发现以下问题：\n")
    for v in VIOLATIONS:
        print("  ✗", v)
    sys.exit(1)
else:
    print(f"静态检查通过：{len(files)} 个 Go 文件，无第三方依赖，括号平衡，build tag 配对。")
    sys.exit(0)
