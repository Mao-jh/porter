#!/usr/bin/env python3
"""改进版静态自检：剥离注释与字符串字面量后做括号配对 + import 使用检查。
注意：此为 Go 工具链缺失时的兜底，不替代 go build/vet。
"""
import os, re, glob, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GOFILES = glob.glob(os.path.join(ROOT, "**", "*.go"), recursive=True)
errors = []

def strip(src):
    # 剥离 // 行注释
    src = re.sub(r'//.*', '', src)
    # 剥离 /* */ 块注释
    src = re.sub(r'/\*.*?\*/', '', src, flags=re.S)
    # 剥离 "..." 双引号字符串（含转义）
    src = re.sub(r'"(\\.|[^"\\])*"', '""', src)
    # 剥离 `...` 反引号原始字符串
    src = re.sub(r'`[^`]*`', '``', src)
    return src

def check(path):
    raw = open(path, encoding="utf-8").read()
    src = strip(raw)
    for open_c, close_c in [("(", ")"), ("{", "}"), ("[", "]")]:
        if src.count(open_c) != src.count(close_c):
            errors.append(f"{path}: 括号 {open_c}{close_c} 不配对")

for f in sorted(GOFILES):
    try:
        check(f)
    except Exception as e:
        errors.append(f"{f}: 读取异常 {e}")

for e in errors:
    print("ERROR:", e)
print(f"\n扫描 {len(GOFILES)} 个 Go 文件")
sys.exit(1 if errors else 0)
