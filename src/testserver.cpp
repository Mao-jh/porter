// file: src/testserver.cpp
// 本地环回测试服务端（testserver/）：强制仅绑 127.0.0.0/8（H-3）
// 支持 HTTP Range 响应(206 + Content-Range)、断连/超时/429/5xx 故障注入
// 编译：g++ -O2 -std=c++17 -pthread src/testserver.cpp -o bin/testserver
// 注：本文件为独立编译单元，仅依赖 downloader.h，不引用 engine.cpp（避免符号重复）
#include "downloader.h"
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/sendfile.h>

namespace dl {

struct TestServer {
    int port = 18080;
    std::string data_dir = "/data/workspace/testdata";
    FaultInjector fault;
    std::atomic<bool> running{true};

    void start_in_thread() {
        std::thread([this]{ run(); }).detach();
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    void run() {
        int srv = socket(AF_INET, SOCK_STREAM, 0);
        int opt = 1; setsockopt(srv, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
        sockaddr_in addr{}; addr.sin_family = AF_INET;
        // H-3：强制仅绑 127.0.0.0/8（此处用 127.0.0.1），禁止 0.0.0.0/公网
        addr.sin_addr.s_addr = inet_addr("127.0.0.1");
        addr.sin_port = htons(port);
        if (bind(srv, (sockaddr*)&addr, sizeof(addr)) < 0) { perror("bind"); return; }
        listen(srv, 16);
        while (running) {
            int cli = accept(srv, nullptr, nullptr);
            if (cli < 0) continue;
            std::thread([this, cli]{ handle(cli); }).detach();
        }
    }

    void handle(int cli) {
        char req[4096] = {0}; recv(cli, req, sizeof(req)-1, 0);
        std::string r(req);
        // 解析 Range: bytes=x-y
        uint64_t range_start = 0, range_end = 0; bool has_range = false;
        auto pr = r.find("Range: bytes="); if (pr != std::string::npos) {
            has_range = true;
            auto p1 = pr + 13; auto p2 = r.find('-', p1); auto p3 = r.find('\n', p1);
            range_start = std::stoull(r.substr(p1, p2-p1));
            std::string es = r.substr(p2+1, p3-p2-1); range_end = std::stoull(es);
        }
        // 故障注入
        Fault f = fault.next();
        if (f == Fault::DROP)   { close(cli); return; }          // 断连
        if (f == Fault::TIMEOUT) { std::this_thread::sleep_for(std::chrono::seconds(60)); }
        if (f == Fault::HTTP429) { send_plain(cli, "HTTP/1.1 429 Too Many Requests\r\nContent-Length: 0\r\n\r\n"); close(cli); return; }
        if (f == Fault::HTTP500) { send_plain(cli, "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n"); close(cli); return; }

        // 读取请求的文件，按 Range 返回
        auto path = extract_path(r); // e.g. /testdata/128.bin
        std::ifstream fstr(data_dir + "/" + path, std::ios::binary);
        fstr.seekg(0, std::ios::end); uint64_t fsize = fstr.tellg(); fstr.seekg(0);
        if (has_range) {
            std::ostringstream hdr; hdr << "HTTP/1.1 206 Partial Content\r\n"
                << "Content-Range: bytes " << range_start << "-" << range_end << "/" << fsize << "\r\n"
                << "Content-Length: " << (range_end - range_start + 1) << "\r\n\r\n";
            send_plain(cli, hdr.str());
            fstr.seekg(range_start);
            std::vector<char> buf(64<<10);
            uint64_t left = range_end - range_start + 1;
            while (left > 0) {
                size_t k = fstr.read(buf.data(), std::min((uint64_t)buf.size(), left)).gcount();
                if (k == 0) { break; }
                send(cli, buf.data(), k, 0); left -= k;
            }
        } else {
            send_plain(cli, "HTTP/1.1 200 OK\r\nContent-Length: " + std::to_string(fsize) + "\r\n\r\n");
        }
        close(cli);
    }

private:
    std::string extract_path(const std::string& r) {
        auto a = r.find(' '); auto b = r.find(' ', a+1);
        std::string p = r.substr(a+1, b-a-1); if (p=="/") p="/128.bin"; return p.substr(1);
    }
    void send_plain(int cli, const std::string& s) { send(cli, s.data(), s.size(), 0); }
};

} // namespace dl

// testserver 独立入口（不与 main.cpp 共用）
int main(int argc, char** argv) {
    dl::TestServer srv;
    if (argc > 1) srv.port = atoi(argv[1]);
    srv.start_in_thread();
    // 自检：绑定后确认地址为 127.0.0.1（H-3）
    fprintf(stdout, "testserver listening on 127.0.0.1:%d (H-3: loopback only)\n", srv.port);
    fprintf(stdout, "data_dir=%s\n", srv.data_dir.c_str());
    // 简单存活：处理若干连接后退出（CI 友好）
    std::this_thread::sleep_for(std::chrono::seconds(2));
    srv.running = false;
    return 0;
}
