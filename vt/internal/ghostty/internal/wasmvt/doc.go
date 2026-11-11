// Package wasmvt is libghostty-vt, compiled to WebAssembly and translated
// to Go by wasm2go. ghostty_vt.go is generated from ghostty-vt.wasm by
// the go:generate line below, which is the only place its flags live.
// Never edit it: run make ghostty-wasm, or go generate after a wasm2go
// bump.
//
// -unsafe lets the generated code load and store through package unsafe.
// Every access is still bounds-checked, and throughput is about 1.8x the
// safe output.
//
// Only package ghostty may import it.
package wasmvt

//go:generate go tool wasm2go -unsafe -pkg wasmvt -o ghostty_vt.go ghostty-vt.wasm
