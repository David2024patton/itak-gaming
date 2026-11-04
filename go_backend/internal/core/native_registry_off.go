//go:build !gocores

package core

// LoadNative is the stub compiled into normal builds,
// including the shipped app: the app carries no bundled
// Go cores (David's order, 1.102; the Rust cores are the
// only cores in the app), so there is never a bundled
// native core to return. The real registry lives in
// native_registry.go under the gocores build tag, used by
// server-side reference and cross-check runs.
func LoadNative(corePath string) (*NativeBridge, bool) {
	return nil, false
}
