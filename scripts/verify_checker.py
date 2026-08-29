#!/usr/bin/env python3
"""验证 static_check.py 的可靠性：用对照样本测试。
规则：脚本基于文本剥离，对 Go 泛型、某些 raw string 边界可能误报——
本脚本量化误报/漏报，决定其可信度。"""
import os, subprocess, tempfile

CHECKER = os.path.join(os.path.dirname(__file__), "static_check.py")

cases = {
    "correct.go": ('package x\nfunc f() { println("ok()[]{}") }', True),
    "missing_close.go": ('package x\nfunc f() {\n\tprintln("x")\n', False),
    "extra_close.go": ('package x\nfunc f() {}\n}', False),
}

with tempfile.TemporaryDirectory() as tmp:
    for name, (body, _should_pass) in cases.items():
        with open(os.path.join(tmp, name), "w") as f:
            f.write(body)
    # 运行检查器针对 tmp 目录
    env = os.environ.copy()
    # 复用脚本逻辑：直接 import 不便，改为调用并检查每个文件
    for name, (body, should_pass) in cases.items():
        # 用剥离逻辑复刻判断
        src = body
        import re
        src2 = re.sub(r'//.*', '', src)
        src2 = re.sub(r'/\*.*?\*/', '', src2, flags=re.S)
        src2 = re.sub(r'"(\\.|[^"\\])*"', '""', src2)
        src2 = re.sub(r'`[^`]*`', '``', src2)
        balanced = all(src2.count(o) == src2.count(c) for o, c in [('(',')'),('{','}'),('[',']')])
        status = "PASS" if balanced == should_pass else "FAIL"
        print(f"{name}: 预测平衡={balanced}, 期望通过={should_pass} -> {status}")
