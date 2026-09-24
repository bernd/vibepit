package wasmvt

// SetMemoryLimitPages caps linear memory growth, in 64 KiB pages. Past the
// cap, memory.grow fails and libghostty sees an allocation failure. The
// generated code keeps the limit in an unexported field; a wasm2go upgrade
// that renames it breaks this file's build, not silently the limit.
func (m *Module) SetMemoryLimitPages(n int64) { m.maxMem = n }
