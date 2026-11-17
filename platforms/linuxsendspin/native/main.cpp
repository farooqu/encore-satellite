// Official Sendspin SDK adapter. No HTTP sink, discovery, guest policy or ALSA.
#include <algorithm>
#include <array>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstring>
#include <deque>
#include <fcntl.h>
#include <filesystem>
#include <iostream>
#include <map>
#include <mutex>
#include <poll.h>
#include <sendspin/client.h>
#include <sendspin/controller_role.h>
#include <sendspin/persistence_codec.h>
#include <sendspin/player_role.h>
#include <stdexcept>
#include <sys/file.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <thread>
#include <unistd.h>

using namespace sendspin;
using Bytes = std::vector<uint8_t>;
// Fixed exit codes carry a diagnosis without exposing SDK logs or secrets.
[[noreturn]] static void fatal(int code = 70) { _exit(code); }
static void check(bool ok) {
  if (!ok)
    throw std::runtime_error("operation failed");
}
// Linux connect() reports actual interface/routing failures asynchronously.
// Maintenance mode must not start any network work at all.
struct Network final : SendspinNetworkProvider {
  bool ready;
  explicit Network(bool value) : ready(value) {}
  bool is_network_ready() override { return ready; }
};
static uint64_t get(const uint8_t *p, size_t n) {
  uint64_t v = 0;
  for (size_t i = 0; i < n; ++i)
    v |= uint64_t(p[i]) << (8 * i);
  return v;
}
static void put(Bytes &b, uint64_t v, size_t n) {
  for (size_t i = 0; i < n; ++i)
    b.push_back(uint8_t(v >> (8 * i)));
}
static void write_all(int fd, const Bytes &b) {
  size_t at = 0;
  while (at < b.size()) {
    ssize_t n = write(fd, b.data() + at, b.size() - at);
    check(n > 0);
    at += size_t(n);
  }
}
static uint64_t checksum(const Bytes &b) {
  uint64_t h = 14695981039346656037ULL;
  for (auto v : b) {
    h ^= v;
    h *= 1099511628211ULL;
  }
  return h;
}

// One atomic, checksummed snapshot, not one file per trust key: revoke-all and
// pairing-secret rotation are a single durable transaction. Owner-only state.
class Store final : public SendspinPersistenceProvider {
  int dir_ = -1, lock_ = -1;
  std::map<std::string, Bytes> values_;
  static bool zero(const Bytes &b) {
    return std::all_of(b.begin(), b.end(), [](auto x) { return x == 0; });
  }
  void validate() {
    using namespace persistence_keys;
    check(values_.contains(KEYPAIR) && values_.contains(PAIRING_PSK));
    for (const auto &[k, b] : values_) {
      if (k == KEYPAIR)
        check(b.size() == 32 && !zero(b));
      else if (k == PAIRING_PSK)
        check(decode_pairing_psk(b.data(), b.size()).has_value());
      else if (k == LAST_PLAYED)
        check(b.size() == 32);
      else if (k == OUTPUT_DELAY)
        check(b.size() == 2);
      else if (k == RECORD_ORDER)
        check(b.size() == 12);
      else {
        bool slot = false;
        for (size_t i = 0; i < 12; ++i)
          if (k == record_slot_key(i))
            slot = true;
        check(
            slot && b.size() == 64 &&
            (zero(b) || decode_pairing_record(b.data(), b.size()).has_value()));
      }
    }
  }
  void persist() {
    Bytes b = {'E', 'S', 'S', '1'};
    put(b, values_.size(), 2);
    for (const auto &[k, v] : values_) {
      put(b, k.size(), 2);
      put(b, v.size(), 2);
      b.insert(b.end(), k.begin(), k.end());
      b.insert(b.end(), v.begin(), v.end());
    }
    put(b, checksum(b), 8);
    // An interrupted temporary snapshot is never loaded as identity.
    int fd =
        openat(dir_, "state.new",
               O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC | O_NOFOLLOW, 0600);
    check(fd >= 0);
    try {
      check(fchmod(fd, 0600) == 0);
      write_all(fd, b);
      check(fsync(fd) == 0);
    } catch (...) {
      close(fd);
      throw;
    }
    check(close(fd) == 0);
    check(renameat(dir_, "state.new", dir_, "state") == 0);
    check(fsync(dir_) == 0);
    int marker = openat(dir_, "initialized",
                        O_WRONLY | O_CREAT | O_CLOEXEC | O_NOFOLLOW, 0600);
    check(marker >= 0);
    check(fsync(marker) == 0);
    close(marker);
    check(fsync(dir_) == 0);
  }

public:
  explicit Store(const std::string &path) {
    if (mkdir(path.c_str(), 0700) != 0)
      check(errno == EEXIST);
    auto parent = std::filesystem::path(path).parent_path();
    int parentfd = open(parent.empty() ? "." : parent.c_str(),
                        O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    check(parentfd >= 0);
    check(fsync(parentfd) == 0);
    close(parentfd);
    dir_ = open(path.c_str(), O_DIRECTORY | O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    check(dir_ >= 0);
    struct stat st{};
    check(fstat(dir_, &st) == 0 && st.st_uid == geteuid() &&
          (st.st_mode & 077) == 0);
    lock_ =
        openat(dir_, "lock", O_RDWR | O_CREAT | O_CLOEXEC | O_NOFOLLOW, 0600);
    check(lock_ >= 0 && flock(lock_, LOCK_EX | LOCK_NB) == 0);
    int fd = openat(dir_, "state", O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) {
      check(errno == ENOENT);
      check(faccessat(dir_, "initialized", F_OK, AT_SYMLINK_NOFOLLOW) < 0 &&
            errno == ENOENT);
      // Seed both required keys in one transaction, using the SDK's
      // public raw-private-key layout and pairing-PSK codec. No SDK
      // provision-on-missing path is permitted after this first boot.
      Bytes identity(32);
      check(getrandom(identity.data(), identity.size(), 0) ==
            ssize_t(identity.size()));
      SendspinPairingPsk p;
      check(getrandom(p.psk.data(), p.psk.size(), 0) == ssize_t(p.psk.size()));
      auto encoded = encode_pairing_psk(p);
      values_[persistence_keys::KEYPAIR] = identity;
      values_[persistence_keys::PAIRING_PSK] =
          Bytes(encoded.begin(), encoded.end());
      validate();
      persist();
      return;
    }
    check(fstat(fd, &st) == 0 && S_ISREG(st.st_mode) &&
          st.st_uid == geteuid() && (st.st_mode & 077) == 0 &&
          st.st_size >= 14 && st.st_size < 8192);
    Bytes b(size_t(st.st_size));
    size_t at = 0;
    while (at < b.size()) {
      ssize_t n = read(fd, b.data() + at, b.size() - at);
      check(n > 0);
      at += size_t(n);
    }
    close(fd);
    check(std::memcmp(b.data(), "ESS1", 4) == 0);
    uint64_t sum = get(b.data() + b.size() - 8, 8);
    b.resize(b.size() - 8);
    check(sum == checksum(b));
    size_t count = get(b.data() + 4, 2);
    at = 6;
    for (size_t i = 0; i < count; ++i) {
      check(at + 4 <= b.size());
      size_t k = get(b.data() + at, 2), v = get(b.data() + at + 2, 2);
      at += 4;
      check(k > 0 && k <= 12 && at + k + v <= b.size());
      std::string key(b.begin() + at, b.begin() + at + k);
      at += k;
      check(!values_.contains(key));
      values_[key] = Bytes(b.begin() + at, b.begin() + at + v);
      at += v;
    }
    check(at == b.size());
    validate();
  }
  ~Store() override {
    if (lock_ >= 0)
      close(lock_);
    if (dir_ >= 0)
      close(dir_);
  }
  std::optional<Bytes> load_blob(const std::string &k) override {
    auto it = values_.find(k);
    if (it == values_.end())
      return std::nullopt;
    return it->second;
  }
  bool save_blob(const std::string &k, const uint8_t *data,
                 size_t len) override {
    // Never let the SDK continue with trust that failed to reach stable
    // storage.
    try {
      check(k != persistence_keys::KEYPAIR &&
            k != persistence_keys::PAIRING_PSK);
      values_[k] = Bytes(data, data + len);
      validate();
      persist();
      return true;
    } catch (...) {
      fatal();
    }
  }
  void revoke() {
    if (values_.empty())
      return;
    auto identity = values_.at(persistence_keys::KEYPAIR);
    SendspinPairingPsk p;
    check(getrandom(p.psk.data(), p.psk.size(), 0) == ssize_t(p.psk.size()));
    auto encoded = encode_pairing_psk(p);
    values_.clear();
    values_[persistence_keys::KEYPAIR] = identity;
    values_[persistence_keys::PAIRING_PSK] =
        Bytes(encoded.begin(), encoded.end());
    validate();
    persist();
  }
};

// v1: 32-byte little-endian header: ESPN, u16 version, u16 type,
// u64 epoch, u64 request/timestamp, u32 payload bytes, u32 value.
static Bytes frame(uint16_t kind, uint64_t epoch = 0, uint64_t id = 0,
                   uint32_t value = 0, const uint8_t *data = nullptr,
                   size_t n = 0) {
  Bytes b = {'E', 'S', 'P', 'N'};
  put(b, 1, 2);
  put(b, kind, 2);
  put(b, epoch, 8);
  put(b, id, 8);
  put(b, n, 4);
  put(b, value, 4);
  if (n)
    b.insert(b.end(), data, data + n);
  return b;
}
class Sink final : public PlayerRoleListener {
  std::mutex mu_;
  std::condition_variable cv_;
  uint64_t generation_ = 0, active_ = 0, request_ = 0, pending_ = 0;
  size_t requested_ = 0;
  // Includes the one packet in flight to Go: at most 40 ms of owned PCM.
  std::deque<Bytes> queued_;
  std::chrono::steady_clock::time_point ack_deadline_;
  uint8_t volume_ = 100;
  bool muted_ = false;
  void send_locked(const Bytes &b) {
    if (send(3, b.data(), b.size(), MSG_DONTWAIT | MSG_NOSIGNAL) !=
        ssize_t(b.size()))
      fatal(71);
  }

public:
  PlayerRole *player = nullptr;
  size_t on_audio_write(uint8_t *data, size_t length,
                        uint32_t timeout) override {
    auto deadline =
        std::chrono::steady_clock::now() + std::chrono::milliseconds(timeout);
    std::unique_lock lock(mu_);
    if (!active_)
      return 0;
    // SDK acceptance is into this bounded native queue, not an IPC round trip.
    // Its 20 ms callback deadline cannot also bound scheduling on another
    // process (or the SDK main loop's fsync). Never claim unowned PCM.
    auto e = active_;
    if (!cv_.wait_until(lock, deadline,
                        [&] { return queued_.size() < 4 || active_ != e; }) ||
        active_ != e)
      return 0;
    size_t n = std::min(length, size_t(1920));
    n -= n % 4;
    if (!n)
      return 0;
    queued_.emplace_back(data, data + n);
    return n;
  }
  void on_stream_start() override {
    std::lock_guard lock(mu_);
    const auto &p = player->get_current_stream_params();
    if (p.codec != SendspinCodecFormat::PCM || p.sample_rate != 48000 ||
        p.channels != 2 || p.bit_depth != 16)
      fatal(73);
    if (active_)
      send_locked(frame(2, active_));
    active_ = ++generation_;
    pending_ = 0;
    queued_.clear();
    send_locked(frame(1, active_));
    cv_.notify_all();
  }
  void on_stream_end() override {
    std::lock_guard lock(mu_);
    if (active_)
      send_locked(frame(2, active_));
    active_ = 0;
    pending_ = 0;
    queued_.clear();
    cv_.notify_all();
  }
  void on_volume_changed(uint8_t v) override {
    std::lock_guard lock(mu_);
    volume_ = v;
    send_locked(frame(3, 0, muted_ ? 1 : 0, volume_));
  }
  void on_mute_changed(bool m) override {
    std::lock_guard lock(mu_);
    muted_ = m;
    send_locked(frame(3, 0, muted_ ? 1 : 0, volume_));
  }
  void receive(ControllerRole &controller) {
    uint8_t b[33];
    iovec iov{b, sizeof(b)};
    msghdr msg{};
    msg.msg_iov = &iov;
    msg.msg_iovlen = 1;
    ssize_t n = recvmsg(3, &msg, MSG_DONTWAIT);
    if (n < 0 && (errno == EAGAIN || errno == EINTR))
      return;
    if (n != 32 || (msg.msg_flags & MSG_TRUNC) ||
        std::memcmp(b, "ESPN", 4) != 0 || get(b + 4, 2) != 1 ||
        get(b + 24, 4) != 0)
      fatal(74);
    auto kind = get(b + 6, 2), epoch = get(b + 8, 8), id = get(b + 16, 8),
         value = get(b + 28, 4);
    std::lock_guard lock(mu_);
    if (kind == 5) {
      if (!epoch || epoch > generation_ || !id || value % 4)
        fatal(74);
      if (epoch != active_)
        return;
      if (id != pending_ || (value != 0 && value != requested_))
        fatal(74);
      if (queued_.empty())
        fatal(74);
      if (value != 0)
        queued_.pop_front();
      pending_ = 0;
      cv_.notify_all();
    } else if (kind == 6) {
      if (!epoch || epoch > generation_ || !id || !value ||
          id > uint64_t(INT64_MAX))
        fatal(74);
      if (epoch == active_)
        player->notify_audio_played(uint32_t(value), int64_t(id));
    } else if (kind == 7) {
      if (epoch || id || (value != 1 && value != 2))
        fatal(74);
      controller.send_command(
          {.command = value == 1 ? SendspinControllerCommand::PLAY
                                 : SendspinControllerCommand::STOP});
    } else
      fatal(74);
  }
  // Runs independently of SDK main-loop persistence and lifecycle dispatch.
  // IPCWorker is declared after the client, so it joins before client
  // destruction.
  void service(ControllerRole &controller, const std::atomic<bool> &stop) {
    while (!stop.load()) {
      {
        std::lock_guard lock(mu_);
        auto now = std::chrono::steady_clock::now();
        if (pending_ && now >= ack_deadline_)
          fatal(72);
        if (active_ && !pending_ && !queued_.empty()) {
          const auto &b = queued_.front();
          pending_ = ++request_;
          requested_ = b.size();
          ack_deadline_ = now + std::chrono::milliseconds(250);
          send_locked(frame(4, active_, pending_, 10, b.data(), b.size()));
        }
      }
      pollfd p{3, POLLIN, 0};
      int n = poll(&p, 1, 2);
      if (n < 0 && errno != EINTR)
        fatal();
      if (p.revents & (POLLHUP | POLLERR | POLLNVAL))
        fatal();
      if (p.revents & POLLIN)
        receive(controller);
    }
  }
};

class IPCWorker {
  std::atomic<bool> stop_{false};
  std::thread worker_;

public:
  IPCWorker(Sink &sink, ControllerRole &controller)
      : worker_(
            [this, &sink, &controller] { sink.service(controller, stop_); }) {}
  ~IPCWorker() {
    stop_.store(true);
    worker_.join();
  }
};

static bool valid_url(const std::string &u) {
  if (!u.starts_with("ws://") || !u.ends_with("/sendspin") ||
      u.find_first_of("@?#\\ \t\r\n") != std::string::npos)
    return false;
  auto host = u.substr(5, u.size() - 14);
  auto colon = host.rfind(':');
  if (colon == std::string::npos || colon == 0 || colon + 1 == host.size())
    return false;
  auto port = host.substr(colon + 1);
  if (port.size() > 5 ||
      port.find_first_not_of("0123456789") != std::string::npos)
    return false;
  int n = std::stoi(port);
  return n > 0 && n <= 65535 &&
         host.substr(0, colon).find('/') == std::string::npos;
}
int main(int argc, char **argv) {
  umask(0077);
  SendspinClient::set_log_level(LogLevel::NONE);
  try {
    check(argc >= 4);
    std::string mode = argv[1], state, name, url;
    check(mode == "run" || mode == "pairing-token" || mode == "revoke-all");
    for (int i = 2; i < argc; i += 2) {
      check(i + 1 < argc);
      std::string k = argv[i];
      if (k == "--state")
        state = argv[i + 1];
      else if (k == "--name")
        name = argv[i + 1];
      else if (k == "--url")
        url = argv[i + 1];
      else
        check(false);
    }
    check(!state.empty());
    if (mode == "run") {
      check(!name.empty() && valid_url(url));
      int type = 0;
      socklen_t len = sizeof(type);
      check(getsockopt(3, SOL_SOCKET, SO_TYPE, &type, &len) == 0 &&
            type == SOCK_SEQPACKET);
      sockaddr_storage address{};
      len = sizeof(address);
      check(getsockname(3, reinterpret_cast<sockaddr *>(&address), &len) == 0 &&
            address.ss_family == AF_UNIX);
    } else
      check(url.empty() && name.empty());
    Store store(state);
    if (mode == "revoke-all") {
      store.revoke();
      return 0;
    }
    // Listener and persistence provider are declared before the client and
    // therefore outlive all SDK role destructors and background tasks.
    Sink sink;
    Network network(mode == "run");
    timespec raw{};
    check(clock_gettime(CLOCK_MONOTONIC_RAW, &raw) == 0);
    SendspinClientConfig cfg;
    cfg.name = name.empty() ? "Echo" : name;
    cfg.accept_inbound_connections = false;
    cfg.pairing_psk_locations = {"operator"};
    SendspinClient client(cfg);
    client.set_network_provider(&network);
    client.set_persistence_provider(&store);
    client.set_unpaired_access_enabled(false);
    PlayerRoleConfig player_cfg;
    player_cfg.audio_formats = {{SendspinCodecFormat::PCM, 2, 48000, 16}};
    auto &player = client.add_player(player_cfg);
    sink.player = &player;
    player.set_listener(&sink);
    player.set_output_delay_adjustable(false);
    auto &controller = client.add_controller();
    check(client.start());
    if (mode == "pairing-token") {
      auto token = client.client_pairing_token();
      check(token.has_value());
      std::cout.write(token->data(), token->size());
      std::cout << '\n';
      std::cout.flush();
      check(bool(std::cout));
      client.stop();
      return 0;
    }
    IPCWorker worker(sink, controller);
    client.connect_to(url);
    for (;;) {
      client.loop();
      std::this_thread::sleep_for(std::chrono::milliseconds(2));
    }
  } catch (...) {
    std::cerr << "sendspin: companion operation failed\n";
    return 70;
  }
}
