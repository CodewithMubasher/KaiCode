//go:build !wasmtime

package deepseek

import "fmt"

// Pow is a stub when building without CGO/wasmtime. DeepSeek's
// proof-of-work challenge solver lives in a WebAssembly module (sha3.wasm)
// driven by the wasmtime-go CGO binding, so it cannot run without CGO.
// Rebuild with `-tags wasmtime` (and a C toolchain + libwasmtime) to enable it.
type Pow struct{}

func NewPow(wasmPath string) (*Pow, error) {
	return nil, fmt.Errorf("DeepSeek proof-of-work solver requires CGO/wasmtime: rebuild with -tags wasmtime (C toolchain and libwasmtime required), or use the mock provider")
}

func (p *Pow) SolveChallenge(challenge, salt string, expireAt int64, difficulty int) (int, error) {
	return 0, fmt.Errorf("DeepSeek proof-of-work solver requires CGO/wasmtime: rebuild with -tags wasmtime")
}

func PowHeader(ch map[string]interface{}, answer int) string {
	return ""
}
