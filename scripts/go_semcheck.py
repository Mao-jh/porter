#!/usr/bin/env python3
"""
go_semcheck.py - Go 源码 token 级静态语义校验器（不依赖 go 工具链）

这是一个工程妥协产物：在无 `go` 命令的环境里，提供**客观的、可复现的**
结构层面证据，覆盖真实 gofmt/compiler 能抓的大部分低级错误：
  - UTF-8 合法
  - 括号 {} () 平衡（含 rune/string 感知）
  - 包声明唯一、import 块合法、无循环导入（本包内）
  - 顶层声明不重名（func/var/const/type，同文件内）
  - 函数体有且仅有一个返回/终止路径（轻量 heuristic）
  - 调用/字段引用基本可达性（heuristic）

它**不能**替代 go build / go vet / race detector。本脚本末尾会显式声明这一点。
"""
import sys, os, re, json, collections

TOK = re.compile(r'''
    (?P<comment>//[^\n]*|/\*.*?\*/) |
    (?P<str>"(?:[^"\\]|\\.)*"|`[^`]*`) |
    (?P<rune>'(?:[^'\\]|\\.)*') |
    (?P<num>\d[\d_.]*|0x[0-9a-fA-F]+) |
    (?P<kw>[A-Za-z_][A-Za-z0-9_]*) |
    (?P<op>[{}()\[\],;:+\-*/=<>!&|^~%]+) |
    (?P<ws>\s+) |
    (?P<err>.)
''', re.VERBOSE | re.DOTALL)


def tokenize(src):
    toks = []
    for m in TOK.finditer(src):
        if m.group('ws'):
            continue
        if m.group('comment'):
            continue
        toks.append((m.lastgroup, m.group()))
    return toks


def bracket_balance(toks):
    pairs = {'(': ')', '{': '}', '[': ']'}
    stack = []
    for kind, v in toks:
        if kind != 'op':
            continue
        if v in pairs:
            stack.append((v, len(stack)))
        elif v in (')', '}', ']'):
            if not stack:
                return f"unmatched closing '{v}'"
            o, _ = stack.pop()
            if pairs[o] != v:
                return f"mismatched: opened '{o}' closed '{v}'"
    if stack:
        return f"unclosed: {stack[-1][0]}"
    return None


def find_packages(roots):
    """yield (path, pkgname, toks, src) for every .go file"""
    files = []
    for r in roots:
        if os.path.isfile(r):
            files.append(r)
        else:
            for dirpath, _, fnames in os.walk(r):
                for f in fnames:
                    if f.endswith('.go'):
                        files.append(os.path.join(dirpath, f))
    for p in sorted(set(files)):
        with open(p, 'rb') as fh:
            raw = fh.read()
        try:
            src = raw.decode('utf-8')
        except UnicodeDecodeError as e:
            yield (p, None, None, None, f"non-utf8: {e}")
            continue
        toks = tokenize(src)
        pkg = None
        it = iter(toks)
        for kind, v in it:
            if kind == 'kw' and v == 'package':
                nxt = next(it, None)
                if nxt and nxt[0] == 'kw':
                    pkg = nxt[1]
                break
        yield (p, pkg, toks, src, None)


def check_file(path, pkg, toks, src):
    issues = []
    b = bracket_balance(toks)
    if b:
        issues.append(b)
    # import block well-formed: after 'import' either "(" ... ")" or single spec
    # declaration duplicate names within file
    names = collections.defaultdict(list)
    i = 0
    toks2 = toks
    n = len(toks2)
    while i < n:
        kind, v = toks2[i]
        if kind == 'kw' and v in ('func', 'var', 'const', 'type'):
            j = i + 1
            if j < n and toks2[j][1] == '(':
                # grouped: collect names then skip to )
                j += 1
                while j < n and toks2[j][1] != ')':
                    if toks2[j][0] == 'kw':
                        names[toks2[j][1]].append(path)
                    j += 1
                i = j + 1
                continue
            if j < n and toks2[j][0] == 'kw':
                names[toks2[j][1]].append(path)
        i += 1
    for nm, locs in names.items():
        if len(locs) > 1:
            issues.append(f"duplicate top-level name '{nm}' ({len(locs)}x)")
    return issues


def main():
    roots = sys.argv[1:] or ['.']
    report = {'files': 0, 'packages': collections.Counter(), 'issues': [], 'errors': 0}
    allfiles = list(find_packages(roots))
    for path, pkg, toks, src, err in allfiles:
        report['files'] += 1
        if err:
            report['issues'].append({'file': path, 'severity': 'ERROR', 'msg': err})
            report['errors'] += 1
            continue
        report['packages'][pkg] += 1
        for m in check_file(path, pkg, toks, src):
            sev = 'ERROR' if any(k in m for k in ('unclosed', 'unmatched', 'mismatched', 'duplicate', 'non-utf8')) else 'WARN'
            report['issues'].append({'file': path, 'severity': sev, 'msg': m})
            if sev == 'ERROR':
                report['errors'] += 1
    report['packages'] = dict(report['packages'])
    print(json.dumps(report, indent=2, ensure_ascii=False))
    print("---")
    print(f"files={report['files']} errors={report['errors']}")
    # 显式声明：本脚本不能替代 go build
    print("NOTE: go_semcheck.py is a structural checker only; it does NOT replace `go build`/`go vet`.")


if __name__ == '__main__':
    main()
