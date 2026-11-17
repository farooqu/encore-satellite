// Generate pooled.golden with the unchanged firmware executor. Usage:
// g++ -O2 -std=c++17 -I$COMPONENT reference.cpp $COMPONENT/mixednet.cpp \
//   $COMPONENT/mixednet_blob.cpp -o /tmp/mixednet-reference
// /tmp/mixednet-reference pooled.mnb > pooled.golden
#include "mixednet.h"
#include "mixednet_blob.h"
#include <cstdio>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <vector>

int main(int argc, char **argv) {
  using namespace esphome::voice_incubator;
  if (argc != 2) return 2;
  std::ifstream file(argv[1], std::ios::binary);
  std::vector<uint8_t> bytes((std::istreambuf_iterator<char>(file)), {});
  MixedNetBlob blob;
  if (!blob.parse(bytes.data(), bytes.size())) return 3;
  MixedNetExecutor executor;
  if (!executor.setup(blob.model())) return 4;
  std::vector<int8_t> features(blob.input_frames() * 40);
  uint32_t seed = 1234;
  const int8_t constants[] = {0, -128, 127};
  for (int i = 0; i < 3000; ++i) {
    for (auto &v : features) {
      seed = seed * 1664525u + 1013904223u;
      v = static_cast<int8_t>(seed >> 24);
      if (i < 3) v = constants[i];
    }
    const uint8_t probability = executor.invoke(features.data());
    std::putchar(probability);
    std::putchar(static_cast<uint8_t>(executor.last_logit()));
  }
}
