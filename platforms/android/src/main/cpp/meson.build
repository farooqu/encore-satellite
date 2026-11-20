project('encore-aec', 'cpp', default_options: ['cpp_std=c++17'])

apm = subproject('webrtc-audio-processing-1.3').get_variable('audio_processing_dep')
shared_library('encore_aec', 'voice_aec.cpp', dependencies: apm,
  cpp_args: ['-DWEBRTC_POSIX', '-DWEBRTC_ANDROID'],
  link_args: ['-static-libstdc++', '-Wl,--exclude-libs,ALL'], gnu_symbol_visibility: 'hidden')
