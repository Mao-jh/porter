#!/usr/bin/env python3
"""对每个报告文件，做逐字符扫描打印首个不平衡位置，辅助定位。"""
import os, re

files = [
    "cli/cli.go", "cli/cli_test.go", "persist/persist_test.go", "testserver/server.go",
]
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

def strip(src):
    src = re.sub(r'//.*', '', src)
    src = re.sub(r'/\*.*?\*/', '', src, flags=re.S)
    src = re.sub(r'"(\\.|[^"\\])*"', '""', src)
    src = re.sub(r'`[^`]*`', '``', src)
    return src

for rel in files:
    path = os.path.join(ROOT, rel)
    src = strip(open(path, encoding="utf-8").read())
    for open_c, close_c in [("{", "}"), ("(", ")"), ("[", "]")]:
        o, c = src.count(open_c), src.count(close_c)
        if o != c:
            print(f"{rel}: {open_c}{close_c} -> {o} vs {c}")
            # 找首个使计数<0的位置
            bal = 0
            for i, ch in enumerate(src):
                if ch == open_c: bal += 1
                elif ch == close_c: bal -= 1
                if bal < 0:
                    print(f"   首个多余 {close_c} 位于字符 ~{i}: ...{repr(src[max(0,i-30):i+10])}...")
                    break
print("done")
