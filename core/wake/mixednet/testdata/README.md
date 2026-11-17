# Mixednet reference fixture

`pooled.mnb` is the unchanged Voice Incubator serializer's output for its
`tests/fixtures/pooled.tflite` at revision
`35611dc9fd3028a1601ee05209981ee46913d264` (31,980 bytes, 21 buffers, 26
operations, 3 input frames, 5,481 arena bytes). It includes max pooling.

Generate it with `voice_incubator.mixednet.pack(Path("tests/fixtures/pooled.tflite"))`
in that checkout (Python dependencies: numpy and tflite).

`pooled.golden` contains 3,000 pairs of probability and raw int8 logit bytes,
produced by `reference.cpp` linked against the **unchanged** firmware
`mixednet.cpp` and `mixednet_blob.cpp` at revision
`d78f7e260918169a3ca6bfc967311e6359a4599c`. The harness documents its build and
invocation. It uses firmware's default gemmlowp double rounding (do not define
MIXEDNET_SINGLE_ROUNDING). Inputs cover all-zero, -128 and +127 strides followed
by deterministic pseudorandom features, 9,000 feature frames total. The Go test
generates exactly the same inputs and compares every probability and logit.

Normal Go tests require neither Python, the other repositories, nor a C++
compiler. The reference harness is only for reproducing the golden file.
