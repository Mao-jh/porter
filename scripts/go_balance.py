#!/usr/bin/env python3
"""
go_balance.py - Minimal Correct Bracket Checker for Go source.

Design: a single character-by-character state machine that correctly skips
comments, runes, and string literals (both "..." and `...`). Inside a string
or comment, brackets have NO semantic meaning and MUST be ignored. This is the
standard "balanced delimiters" algorithm, correct by construction.

We deliberately do NOT try to parse Go. This only proves:
  "If the file is valid Go, its bracket structure is balanced."
A balanced file can still be semantically wrong; an unbalanced file is
definitely broken. This is a sound necessary condition, not a compiler.

Usage: go_balance.py <file.go> ...
       go_balance.py --all <dir>
"""
import sys, os


def is_balanced(src: str):
    """Return (balanced:bool, stack_depth:int, error:str|None)"""
    i = 0
    n = len(src)
    stack = []
    pairs = {'(': ')', '{': '}', '[': ']'}

    def err(msg):
        line = src[:i].count('\n') + 1
        return False, len(stack), f"{msg} at line {line}"

    while i < n:
        c = src[i]
        # line comment
        if c == '/' and i + 1 < n and src[i + 1] == '/':
            i += 2
            while i < n and src[i] != '\n':
                i += 1
            continue
        # block comment
        if c == '/' and i + 1 < n and src[i + 1] == '*':
            i += 2
            while i < n:
                if src[i] == '*' and i + 1 < n and src[i + 1] == '/':
                    i += 2
                    break
                i += 1
            else:
                return err("unterminated block comment")
            continue
        # rune literal
        if c == "'":
            i += 1
            while i < n:
                if src[i] == '\\' and i + 1 < n:
                    i += 2
                    continue
                if src[i] == "'":
                    i += 1
                    break
                i += 1
            else:
                return err("unterminated rune")
            continue
        # string literal
        if c == '"':
            i += 1
            while i < n:
                if src[i] == '\\' and i + 1 < n:
                    i += 2
                    continue
                if src[i] == '"':
                    i += 1
                    break
                i += 1
            else:
                return err("unterminated string")
            continue
        # raw string
        if c == '`':
            i += 1
            while i < n and src[i] != '`':
                i += 1
            if i >= n:
                return err("unterminated raw string")
            i += 1
            continue
        # brackets
        if c in pairs:
            stack.append(c)
        elif c in (')', '}', ']'):
            if not stack:
                return err(f"unexpected closing '{c}'")
            o = stack.pop()
            if pairs[o] != c:
                return err(f"mismatched: opened '{o}' closed '{c}'")
        i += 1

    if stack:
        return False, len(stack), f"unclosed: {stack[-1]}"
    return True, 0, None


def main():
    args = sys.argv[1:]
    if not args:
        print("usage: go_balance.py <file> [files...] | --all <dir>")
        sys.exit(2)
    files = []
    if args[0] == '--all':
        root = args[1] if len(args) > 1 else '.'
        for dp, _, fns in os.walk(root):
            for f in fns:
                if f.endswith('.go'):
                    files.append(os.path.join(dp, f))
    else:
        files = args

    bad = 0
    for p in sorted(files):
        with open(p, 'rb') as fh:
            raw = fh.read()
        try:
            src = raw.decode('utf-8')
        except UnicodeDecodeError as e:
            print(f"ERROR  {p}: non-utf8 ({e})")
            bad += 1
            continue
        ok, depth, err = is_balanced(src)
        if ok:
            print(f"OK     {p}")
        else:
            print(f"ERROR  {p}: {err}")
            bad += 1
    print("---")
    print(f"{len(files)} files, {bad} unbalanced")
    print("NOTE: bracket balance is a necessary (not sufficient) condition for valid Go.")
    sys.exit(1 if bad else 0)


if __name__ == '__main__':
    main()
