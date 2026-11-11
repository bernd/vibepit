package wasmvt

// MaxMemoryPages is the module's linear memory limit in 64 KiB pages, set
// at link time by build-wasm.sh. Past it, memory.grow fails and libghostty
// sees an allocation failure. It reads the generated constant, so a
// wasm2go upgrade that renames it breaks this file's build, not silently
// the limit.
const MaxMemoryPages = maxMem
