#include <jni.h>

#include <array>
#include <cstdint>
#include <memory>

#include "modules/audio_processing/include/audio_processing.h"

namespace {
constexpr int kSamples = 160;
constexpr int kBytes = kSamples * 2;
using Block = std::array<int16_t, kSamples>;

class VoiceAec {
 public:
  VoiceAec() {
    webrtc::AudioProcessing::Config config;
    config.echo_canceller.enabled = true;
    config.echo_canceller.mobile_mode = false;  // AEC3, not AECM.
    apm_ = webrtc::AudioProcessingBuilder().Create();
    if (apm_) apm_->ApplyConfig(config);
  }

  bool Reset() { return apm_ && apm_->Initialize() == 0; }

  bool Process(Block& render, Block& capture) {
    // Reverse and forward are already aligned to the same presentation/capture
    // interval by the host. AEC3 estimates the remaining acoustic path delay.
    const webrtc::StreamConfig format(16000, 1);
    return apm_->ProcessReverseStream(render.data(), format, format, render.data()) == 0 &&
           apm_->set_stream_delay_ms(0) == 0 &&
           apm_->ProcessStream(capture.data(), format, format, capture.data()) == 0;
  }

 private:
  rtc::scoped_refptr<webrtc::AudioProcessing> apm_;
};

Block ReadBlock(JNIEnv* env, jbyteArray data) {
  std::array<jbyte, kBytes> bytes{};
  env->GetByteArrayRegion(data, 0, kBytes, bytes.data());
  Block pcm;
  for (int i = 0; i < kSamples; ++i) {
    pcm[i] = static_cast<int16_t>(static_cast<uint8_t>(bytes[2 * i]) |
                                 (static_cast<uint8_t>(bytes[2 * i + 1]) << 8));
  }
  return pcm;
}
}  // namespace

extern "C" JNIEXPORT jlong JNICALL Java_com_encore_satellite_VoiceAec_create(JNIEnv*, jobject) {
  auto aec = std::make_unique<VoiceAec>();
  return aec->Reset() ? reinterpret_cast<jlong>(aec.release()) : 0;
}

extern "C" JNIEXPORT jboolean JNICALL Java_com_encore_satellite_VoiceAec_reset(JNIEnv*, jobject, jlong handle) {
  return reinterpret_cast<VoiceAec*>(handle)->Reset();
}

extern "C" JNIEXPORT jboolean JNICALL Java_com_encore_satellite_VoiceAec_process(
    JNIEnv* env, jobject, jlong handle, jbyteArray render, jbyteArray capture) {
  if (env->GetArrayLength(render) != kBytes || env->GetArrayLength(capture) != kBytes) return false;
  auto far = ReadBlock(env, render);
  auto near = ReadBlock(env, capture);
  if (env->ExceptionCheck() || !reinterpret_cast<VoiceAec*>(handle)->Process(far, near)) return false;
  std::array<jbyte, kBytes> output;
  for (int i = 0; i < kSamples; ++i) {
    output[2 * i] = static_cast<jbyte>(near[i]);
    output[2 * i + 1] = static_cast<jbyte>(static_cast<uint16_t>(near[i]) >> 8);
  }
  env->SetByteArrayRegion(capture, 0, kBytes, output.data());
  return !env->ExceptionCheck();
}

extern "C" JNIEXPORT void JNICALL Java_com_encore_satellite_VoiceAec_destroy(JNIEnv*, jobject, jlong handle) {
  delete reinterpret_cast<VoiceAec*>(handle);
}
