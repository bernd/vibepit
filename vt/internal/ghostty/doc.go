// Package ghostty binds libghostty-vt, compiled to WebAssembly and
// translated to Go by wasm2go (package wasmvt). Its functions mirror the C
// API one to one and use its terms. It keeps no state beyond one module
// instance and does no locking.
//
// Only package vt may import it. The feature tests in this package pin
// every libghostty behaviour vibepit relies on; see README.md before
// upgrading the module or wasm2go.
package ghostty
