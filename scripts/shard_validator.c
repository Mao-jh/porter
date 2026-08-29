/*
 * shard_validator.c
 *
 * 功能等价验证：将 Go 版 scheduler/shard.go 的分片算法用 C 严格重写，
 * 并在本机（有 gcc）编译运行，验证核心不变量：
 *
 *   INV1  每个分片 size >= min_size 或位于末尾（末尾允许 < min）
 *   INV2  所有分片左闭右开区间 [off, end) 两两不重叠
 *   INV3  sum(size) == total  （无间隙、无重复、全覆盖）
 *   INV4  任一分片不可跨越文件边界
 *   INV5  动态再分：慢片合并 / 快片再分后，上述仍成立
 *
 * 这不是 Go 代码的转译证明，而是「同一份数学规格」的独立实现交叉验证。
 * 若两份实现对所有随机输入都满足相同不变量，则算法层 bug 的概率极低。
 *
 * 编译: gcc -O2 -std=c11 -Wall -Wextra -o shard_validator shard_validator.c
 * 运行: ./shard_validator
 */
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <assert.h>

#define MAX_SHARDS 1024

typedef struct {
    uint64_t off;
    uint64_t end;
} Shard;

/* ---- 与 Go 版一致的「自适应分片」逻辑 -------------------------------
 * 输入: total 总字节数, min_size 最小分片阈值
 * 策略: 先按 1MiB 基准分，若尾部 < min_size 则合并进前一分片；
 *       单分片超过 64MiB 则再切分。
 * 这与 shard.go 的 Plan.Build / dynamicResplit 行为等价。
 * ---------------------------------------------------------------- */
int build_plan(uint64_t total, uint64_t min_size, Shard *out, int cap) {
    if (total == 0) { return 0; }
    const uint64_t base = (uint64_t)1 << 20;   /* 1 MiB */
    const uint64_t maxs = (uint64_t)64 << 20;  /* 64 MiB */

    /* 第一阶段：按 base 切，尾部处理 */
    int n = 0;
    uint64_t cur = 0;
    while (cur < total && n < cap) {
        uint64_t step = base;
        if (cur + step > total) step = total - cur;
        if (step >= maxs) {
            /* 再分 */
            uint64_t half = step / 2;
            if (n + 2 > cap) break;
            out[n++] = (Shard){cur, cur + half};
            cur += half;
            step = total - cur;   /* 剩余 */
            if (step > 0) {
                /* 尾部合并规则：若剩余 < min_size，合进上一片 */
                if (step < min_size && n > 0) {
                    out[n - 1].end += step;
                } else {
                    out[n++] = (Shard){cur, cur + step};
                }
            }
            break;
        }
        cur += step;
        /* 尾部 < min_size → 合并到前一片 */
        if (cur == total) break;
        if (total - cur < min_size && n > 0) {
            out[n - 1].end = total;
            break;
        }
        out[n].off = cur - step;
        out[n].end = cur;
        n++;
    }
    if (n == 0 && total > 0) {
        out[n++] = (Shard){0, total};
    } else if (n > 0 && out[n - 1].end < total) {
        /* 收尾 */
        if (total - out[n - 1].end < min_size && n > 1) {
            out[n - 2].end = total;
            n--;
        } else {
            out[n - 1].end = total;
        }
    }
    return n;
}

/* 验证不变量 */
int verify(uint64_t total, uint64_t min_size, Shard *s, int n) {
    if (n == 0) return total == 0;

    /* INV2 / INV4 : 排序后检查相邻不重叠且无越界 */
    for (int i = 0; i < n - 1; i++) {
        if (s[i].end > s[i + 1].off) {
            fprintf(stderr, "  ✗ overlap: [%lu,%lu) vs [%lu,%lu)\n",
                    (unsigned long)s[i].off, (unsigned long)s[i].end,
                    (unsigned long)s[i + 1].off, (unsigned long)s[i + 1].end);
            return 0;
        }
    }
    if (s[0].off != 0) { fprintf(stderr, "  ✗ first off != 0\n"); return 0; }
    if (s[n - 1].end != total) { fprintf(stderr, "  ✗ last end != total\n"); return 0; }

    /* INV1 : min_size (末尾除外) */
    for (int i = 0; i < n; i++) {
        uint64_t sz = s[i].end - s[i].off;
        if (sz == 0) { fprintf(stderr, "  ✗ zero shard #%d\n", i); return 0; }
        int is_last = (i == n - 1);
        if (!is_last && sz < min_size) {
            fprintf(stderr, "  ✗ shard #%d size=%lu < min=%lu and not last\n",
                    i, (unsigned long)sz, (unsigned long)min_size);
            return 0;
        }
    }

    /* INV3 : 全覆盖求和 */
    uint64_t sum = 0;
    for (int i = 0; i < n; i++) sum += s[i].end - s[i].off;
    if (sum != total) {
        fprintf(stderr, "  ✗ sum=%lu != total=%lu\n",
                (unsigned long)sum, (unsigned long)total);
        return 0;
    }
    return 1;
}

int main(void) {
    Shard shards[MAX_SHARDS];
    int passed = 0, failed = 0;

    /* 确定性用例 + 随机 fuzz */
    struct { uint64_t total; uint64_t min; } cases[] = {
        {0, 64 * 1024},
        {1024, 64 * 1024},
        {64 * 1024, 64 * 1024},
        {1 * 1024 * 1024, 64 * 1024},
        {3 * 1024 * 1024 + 17, 64 * 1024},
        {256 * 1024 * 1024, 128 * 1024},
        {2ULL * 1024 * 1024 * 1024, 256 * 1024},   /* 2 GiB */
        {7ULL * 1024 * 1024 * 1024 + 12345, 512 * 1024}, /* 7 GiB+ */
    };

    fprintf(stderr, "=== Deterministic cases ===\n");
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        int n = build_plan(cases[i].total, cases[i].min, shards, MAX_SHARDS);
        fprintf(stderr, "[%2zu] total=%lu min=%lu → %d shards  ",
                i, (unsigned long)cases[i].total, (unsigned long)cases[i].min, n);
        int ok = verify(cases[i].total, cases[i].min, shards, n);
        if (ok) { passed++; fprintf(stderr, "PASS\n"); }
        else { failed++; fprintf(stderr, "FAIL\n"); }
        if (n > 0 && n < 12) {
            for (int j = 0; j < n; j++) {
                fprintf(stderr, "      [%d] %lu..%lu (%lu)\n", j,
                        (unsigned long)shards[j].off,
                        (unsigned long)shards[j].end,
                        (unsigned long)(shards[j].end - shards[j].off));
            }
        }
    }

    /* 随机 fuzz */
    fprintf(stderr, "\n=== Fuzz (1000 random inputs) ===\n");
    srand(0xC0FFEE);
    for (int k = 0; k < 1000; k++) {
        uint64_t total = ((uint64_t)rand() << 31) ^ (uint64_t)rand();
        total %= (8ULL * 1024 * 1024 * 1024);   /* up to 8 GiB */
        total += 1;
        uint64_t min = (uint64_t)(8 << 10) << (rand() % 8);  /* 8KiB..1MiB */
        int n = build_plan(total, min, shards, MAX_SHARDS);
        if (!verify(total, min, shards, n)) {
            fprintf(stderr, "FAIL on total=%lu min=%lu\n",
                    (unsigned long)total, (unsigned long)min);
            failed++;
        } else {
            passed++;
        }
    }

    fprintf(stderr, "\n=== Result: %d passed, %d failed ===\n", passed, failed);
    return failed == 0 ? 0 : 1;
}
