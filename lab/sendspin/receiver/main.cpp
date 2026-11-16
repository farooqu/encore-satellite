// Isolated lab receiver. The upstream SDK owns protocol, Noise and scheduling;
// Encore's loopback virtual mixer owns clocked consumption and temporary gain.
#include "sendspin/client.h"
#include "sendspin/controller_role.h"
#include "sendspin/metadata_role.h"
#include "sendspin/player_role.h"
#include "file_persistence_provider.h"
#include <ixwebsocket/IXHttpClient.h>
#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <string>
#include <thread>

using namespace sendspin;

struct Network : SendspinNetworkProvider {
    bool is_network_ready() override { return true; }
};

struct Sink : PlayerRoleListener {
    PlayerRole& player;
    const std::string url;
    const std::string generation;
    std::atomic<uint64_t> stream{0};
    std::atomic<bool> ready{false};

    Sink(PlayerRole& p, std::string port, std::string g)
        : player(p), url("http://127.0.0.1:" + port + "/music/"), generation(g) {}

    bool post(const std::string& path, const std::string& body, uint64_t epoch) {
        // Separate local HTTP connection per request: stream lifecycle/volume
        // callbacks must never wait behind the PCM sync thread's socket mutex.
        ix::HttpClient http;
        auto args = http.createRequest();
        args->connectTimeout = 1;
        args->transferTimeout = 1;
        args->followRedirects = false;
        args->compress = false;
        args->extraHeaders["Content-Type"] = "application/octet-stream";
        args->extraHeaders["X-Encore-Generation"] = generation;
        args->extraHeaders["X-Encore-Stream"] = std::to_string(epoch);
        auto response = http.post(url + path, body, args);
        return response && response->statusCode >= 200 && response->statusCode < 300;
    }

    void on_stream_start() override {
        ready.store(false);
        const auto& params = player.get_current_stream_params();
        if (params.sample_rate != 48000 || params.channels != 2 || params.bit_depth != 16) {
            fprintf(stderr, "Rejected unexpected music format\n");
            return;
        }
        auto epoch = stream.fetch_add(1) + 1;
        ready.store(post("stream", "start", epoch));
    }

    void on_stream_end() override {
        ready.store(false);
        (void)post("stream", "end", stream.load());
    }

    size_t on_audio_write(uint8_t* data, size_t length, uint32_t timeout_ms) override {
        if (!ready.load() || timeout_ms == 0) return 0;
        const auto epoch = stream.load();
        size_t n = std::min(length, size_t(1920)); // At most 10 ms stereo PCM16.
        n -= n % 4;
        if (!post("pcm", std::string(reinterpret_cast<char*>(data), n), epoch)) return 0;
        // HTTP returns only once the virtual mixer has consumed these frames.
        // Includes local IPC delay: useful for a lab, not acoustic sync proof.
        player.notify_audio_played(static_cast<uint32_t>(n / 4), SendspinClient::now_us());
        return n;
    }

    void volume() {
        (void)post("volume", "{\"volume\":" + std::to_string(player.get_volume()/100.0) +
                   ",\"muted\":" + (player.get_muted() ? "true" : "false") + "}", stream.load());
    }
    void on_volume_changed(uint8_t) override { volume(); }
    void on_mute_changed(bool) override { volume(); }
};

int main(int argc, char** argv) {
    if (argc != 4) return 2; // Private state path, loopback sink port, generation.
    SendspinClient::set_log_level(LogLevel::WARN);
    SendspinClientConfig config;
    config.name = "Encore Sendspin lab";
    config.product_name = "Virtual PCM mixer, not hardware";
    config.software_version = "encore-sendspin-lab-1";
    config.accept_inbound_connections = false;
    FilePersistenceProvider persistence(argv[1]);
    Network network;
    SendspinClient client(config);
    client.set_network_provider(&network);
    client.set_persistence_provider(&persistence);
    // Deliberate isolated-lab policy: encrypted unpaired sessions. This is NOT
    // permission to silently downgrade a product or enable legacy plaintext.
    client.set_unpaired_access_enabled(true);
    PlayerRoleConfig playerConfig;
    playerConfig.audio_formats = {{SendspinCodecFormat::PCM, 2, 48000, 16}};
    auto& player = client.add_player(playerConfig);
    Sink sink(player, argv[2], argv[3]);
    player.set_listener(&sink);
    (void)client.add_controller();
    (void)client.add_metadata();
    if (!client.start()) return 1;
    player.update_volume(100);
    sink.volume();
    fprintf(stderr, "Encore receiver started; PCM16/48000/stereo; encrypted unpaired lab policy\n");
    client.connect_to("ws://127.0.0.1:8927/sendspin");
    while (true) {
        client.loop();
        std::this_thread::sleep_for(std::chrono::milliseconds(2));
    }
}
