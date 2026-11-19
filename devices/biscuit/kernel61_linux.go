//go:build biscuit_kernel61

package biscuit

// This binary is only for the separately built, patched 6.1 experiment.
// Never infer the ABI from uname or silently fall back to another privacy node.
const kernel61 = true
const privacyState = "/sys/devices/platform/amz_privacy/privacy_state"
