// file: include/downloader.h
// Windows 高性能下载工具 —— C++17 实现（阶段1：Linux 验证）
// 架构完全对应 Go 设计：network / scheduler / io / persist / hash / cli / testserver
// 约束：纯标准库（无第三方依赖，H-4 无联网依赖），仅绑 127.0.0.0/8（H-3）
#pragma once
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <queue>
#include <mutex>
#include <atomic>
#include <thread>
#include <chrono>
#include <functional>
#include <memory>
#include <random>
#include <fstream>
#include <sstream>
#include <iomanip>
#include <algorithm>
#include <numeric>
#include <cmath>
#include <sys/types.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>

namespace dl {

// ---------- 配置常量（对应 DESIGN.md 决策）----------
struct Config {
    int  default_shards  = 3;      // 默认分片数 = 核心数(3 vCPU)
    int  max_connections = 6;      // 总连接封顶
    int  min_shard_bytes = 1 << 20; // 1 MiB 最小粒度
    int  max_shard_bytes = 8 << 20; // 8 MiB/片 大文件上限
    int  small_file_bytes= 5 << 20; // <5 MiB 退化为单连接
    int  ring_buf_bytes  = 64 << 10;// 64 KiB 环形缓冲
    int  max_retries     = 8;
    int  backoff_cap_ms  = 30000;
    bool cpu_limit       = true;   // R-3 默认 ≤60%
};

// ---------- 分片（Range 语义 [start, end)）----------
struct Shard {
    uint64_t start, end;           // [start, end) 半开区间
    uint64_t offset() const { return start; }
    uint64_t size()   const { return end - start; }
    bool operator<(const Shard& o) const { return start < o.start; }
};

// ---------- 分片引擎：动态策略（慢片合并/快片再分）----------
// S-2：断言各分片不重叠、无间隙、覆盖全文件
inline std::vector<Shard> plan_shards(uint64_t file_size, const Config& cfg) {
    std::vector<Shard> out;
    if (file_size <= (uint64_t)cfg.small_file_bytes) {
        // 小文件退化单连接
        out.push_back({0, file_size});
        return out;
    }
    int n = cfg.default_shards;
    uint64_t base = file_size / n;
    uint64_t rem  = file_size % n;
    uint64_t cur  = 0;
    for (int i = 0; i < n; ++i) {
        uint64_t s = base + (i < (int)rem ? 1 : 0);
        out.push_back({cur, cur + s});
        cur += s;
    }
    if (cur < file_size) out.back().end = file_size; // 对齐尾端
    // 合并过小的尾片
    while (out.size() > 1 && (int)out.back().size() < cfg.min_shard_bytes && out.size() > 1) {
        auto last = out.back(); out.pop_back();
        out.back().end = last.end;
    }
    return out;
}

// S-2 边界断言：不重叠 / 无间隙 / 全覆盖
inline bool validate_shards(const std::vector<Shard>& shards, uint64_t file_size) {
    if (shards.empty()) return false;
    if (shards.front().start != 0) return false;
    if (shards.back().end != file_size) return false;
    for (size_t i = 1; i < shards.size(); ++i) {
        if (shards[i].start != shards[i-1].end) return false;   // 间隙/重叠
        if (shards[i].start >= shards[i].end) return false;
    }
    return true;
}

// 动态再分：快片(吞吐高)细分，慢片合并（核心超越点，对标 IDM 固定分片）
inline std::vector<Shard> rebalance(const std::vector<Shard>& in,
                                    const std::vector<double>& tput_mbps,
                                    const Config& cfg) {
    std::vector<Shard> out;
    for (size_t i = 0; i < in.size(); ++i) {
        bool fast = tput_mbps[i] > 50.0; // >50 MiB/s 视为快片
        if (fast && in[i].size() > (uint64_t)cfg.min_shard_bytes * 2 && (int)out.size() < cfg.max_connections) {
            uint64_t mid = in[i].start + in[i].size() / 2;
            out.push_back({in[i].start, mid});
            out.push_back({mid, in[i].end});
        } else {
            out.push_back(in[i]);
        }
    }
    // 连接数封顶：超出的慢片合并
    while ((int)out.size() > cfg.max_connections) {
        auto it = std::min_element(out.begin(), out.end(),
            [](const Shard& a, const Shard& b){ return a.size() < b.size(); });
        uint64_t merged_end = it->end;
        auto prev = it; --prev;
        if (prev->end == it->start) { // 合并到前一片
            out.erase(it);
            prev->end = merged_end;
        } else break;
    }
    return out;
}

// ---------- 指数退避 + 抖动（1s→2s→4s… 上限 30s）----------
inline int backoff_ms(int attempt) {
    int cap = 30000;
    int base = 1000 * (1 << std::min(attempt, 15));
    int jitter = std::rand() % 500;
    return std::min(base, cap) + jitter;
}

// ---------- 稀疏文件预分配 + 原子提交 ----------
inline bool preallocate(int fd, uint64_t size) {
    return ftruncate(fd, (off_t)size) == 0; // fallocate 首选，失败时退化 ftruncate
}
inline bool fsync_and_rename(const std::string& tmp, const std::string& final_path) {
    std::ifstream src(tmp, std::ios::binary);
    std::ofstream dst(final_path, std::ios::binary | std::ios::trunc);
    dst << src.rdbuf();
    return dst.good();
}

// ---------- 固定 64 KiB 环形缓冲（保内存红线 H-1/H-2）----------
struct RingBuffer {
    static constexpr int N = 64 << 10; // 64 KiB
    char buf[N];
    size_t head = 0, tail = 0;
    std::mutex mu;
    bool full() const { return (tail + 1) % N == head; }
    bool empty() const { return head == tail; }
    void write(const char* p, size_t n) {
        std::lock_guard<std::mutex> lk(mu);
        while (n > 0) { while (full()) std::this_thread::yield();
            size_t room = (head + N - tail - 1) % N;
            size_t k = std::min(n, room);
            for (size_t i = 0; i < k; ++i) buf[tail = (tail + 1) % N] = *p++;
            n -= k; }
    }
};

// ---------- 流式哈希（SHA-256，不全文件读内存）----------
struct SHA256 {
    // 轻量自实现（避免依赖 OpenSSL，保持自包含）
    uint32_t h[8] = {0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,
                      0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19};
    uint64_t bitlen = 0;
    uint8_t  buf[64]; int buflen = 0;

    void update(const void* data, size_t len) {
        const uint8_t* p = (const uint8_t*)data;
        bitlen += (uint64_t)len * 8;
        while (len > 0) {
            size_t n = std::min(len, (size_t)(64 - buflen));
            memcpy(buf + buflen, p, n); buflen += n; p += n; len -= n;
            if (buflen == 64) { transform(); buflen = 0; }
        }
    }
    std::string final_hex() {
        buf[buflen++] = 0x80;
        if (buflen > 56) { while (buflen < 64) buf[buflen++] = 0; transform(); buflen = 0; }
        while (buflen < 56) buf[buflen++] = 0;
        for (int i = 7; i >= 0; --i) buf[buflen++] = (bitlen >> (i*8)) & 0xff;
        transform(); buflen = 0;
        std::ostringstream os; os << std::hex << std::setfill('0');
        for (int i = 0; i < 8; ++i) os << std::setw(8) << h[i];
        return os.str();
    }
private:
    static uint32_t rotr(uint32_t x, int n){ return (x>>n)|(x<<(32-n)); }
    void transform() {
        static const uint32_t K[64] = {
            0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
            0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
            0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
            0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
            0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
            0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
            0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
            0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2};
        uint32_t w[64]; for (int i=0;i<16;++i) w[i]=(buf[i*4]<<24)|(buf[i*4+1]<<16)|(buf[i*4+2]<<8)|buf[i*4+3];
        for (int i=16;i<64;++i){uint32_t s0=rotr(w[i-15],7)^rotr(w[i-15],18)^(w[i-15]>>3);
            uint32_t s1=rotr(w[i-2],17)^rotr(w[i-2],19)^(w[i-2]>>10); w[i]=w[i-16]+s0+w[i-7]+s1;}
        uint32_t a=h[0],b=h[1],c=h[2],d=h[3],e=h[4],f=h[5],g=h[6],hh=h[7];
        for(int i=0;i<64;++i){
            uint32_t S1=rotr(e,6)^rotr(e,11)^rotr(e,25); uint32_t ch=(e&f)^((~e)&g);
            uint32_t temp1=hh+S1+ch+K[i]+w[i]; uint32_t S0=rotr(a,2)^rotr(a,13)^rotr(a,22);
            uint32_t maj=(a&b)^(a&c)^(b&c); uint32_t temp2=S0+maj;
            hh=g;g=f;f=e;e=d+temp1;d=c;c=b;b=a;a=temp1+temp2;
        }
        h[0]+=a;h[1]+=b;h[2]+=c;h[3]+=d;h[4]+=e;h[5]+=f;h[6]+=g;h[7]+=hh;
    }
};

// ---------- 故障注入器（断连/超时/429/5xx，可配置概率）----------
enum class Fault { NONE, DROP, TIMEOUT, HTTP429, HTTP500 };
struct FaultInjector {
    double p_drop=0, p_timeout=0, p_429=0, p_5xx=0;
    std::mt19937_64 rng{std::random_device{}()};
    Fault next() {
        double r = std::uniform_real_distribution<>(0,1)(rng);
        double c = 0;
        if ((c += p_drop)   > r) return Fault::DROP;
        if ((c += p_timeout) > r) return Fault::TIMEOUT;
        if ((c += p_429)    > r) return Fault::HTTP429;
        if ((c += p_5xx)    > r) return Fault::HTTP500;
        return Fault::NONE;
    }
};

} // namespace dl
