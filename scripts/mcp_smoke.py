#!/usr/bin/env python3
"""mcp_smoke.py — MCP stdio 冒烟测试：模拟 AI 客户端与 downloader-mcp 的完整 JSON-RPC 对话。

协议：MCP stdio = 换行分隔 JSON-RPC 2.0。
流程：initialize → initialized → tools/list → download_start → 轮询 download_status 至 done。
退出码：0=通过。
"""
import json
import subprocess
import sys
import time

def main():
    exe, url = sys.argv[1], sys.argv[2]
    out_dir = sys.argv[3]
    proc = subprocess.Popen(
        [exe], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, text=True, encoding="utf-8", bufsize=1,
    )

    def send(obj):
        proc.stdin.write(json.dumps(obj) + "\n")
        proc.stdin.flush()

    def recv_until(want_id, timeout=30.0):
        deadline = time.time() + timeout
        pending = []
        while time.time() < deadline:
            line = proc.stdout.readline()
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            if msg.get("id") == want_id:
                return msg, pending
            if "id" in msg:
                pending.append(msg)
        raise TimeoutError(f"未在 {timeout}s 内收到 id={want_id} 的响应")

    # 1) initialize + initialized 通知
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-03-26", "capabilities": {},
        "clientInfo": {"name": "smoke", "version": "0"}}})
    msg, _ = recv_until(1)
    info = msg["result"]["serverInfo"]
    print("initialize ok:", info["name"], info["version"])
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    # 2) tools/list：应含 4 个工具
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    msg, _ = recv_until(2)
    tools = [t["name"] for t in msg["result"]["tools"]]
    print("tools:", tools)
    assert set(tools) >= {"download_start", "download_status", "download_cancel", "list_tasks"}, tools

    # 3) download_start
    send({"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {
        "name": "download_start", "arguments": {"url": url, "output_dir": out_dir}}})
    msg, _ = recv_until(3)
    result = msg["result"]
    assert not result.get("isError", False), result
    sc = result.get("structuredContent", {})
    task_id = sc.get("task_id")
    print("download_start:", sc)
    assert task_id, sc

    # 4) 轮询 download_status 至 done
    deadline = time.time() + 60
    state = None
    while time.time() < deadline:
        send({"jsonrpc": "2.0", "id": 100 + int(time.time() * 10), "method": "tools/call", "params": {
            "name": "download_status", "arguments": {}}})
        msg, pend = recv_until(100 + int(time.time() * 10))
        for p in pend:  # 丢弃期间过期轮询
            pass
        sc = msg["result"].get("structuredContent", {})
        for t in sc.get("tasks", []):
            if t.get("id") == task_id or t.get("task_id") == task_id:
                state = t.get("state")
        if state == "done":
            break
        time.sleep(0.3)
    print("final state:", state)
    assert state == "done", state

    proc.stdin.close()
    proc.terminate()
    print("MCP STDIO SMOKE OK")

if __name__ == "__main__":
    main()
