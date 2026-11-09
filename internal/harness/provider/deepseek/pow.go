//go:build wasmtime

package deepseek

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"

	"github.com/bytecodealliance/wasmtime-go/v23"
)

type Pow struct {
	store  *wasmtime.Store
	mem    *wasmtime.Memory
	solve  *wasmtime.Func
	hash   *wasmtime.Func
	malloc *wasmtime.Func
	sp     *wasmtime.Func
}

func NewPow(wasmPath string) (*Pow, error) {
	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("sha3.wasm not found at %s. Please copy it from the python project", wasmPath)
	}
	engine := wasmtime.NewEngine()
	store := wasmtime.NewStore(engine)
	module, err := wasmtime.NewModuleFromFile(engine, wasmPath)
	if err != nil {
		return nil, err
	}
	inst, err := wasmtime.NewInstance(store, module, []wasmtime.AsExtern{})
	if err != nil {
		return nil, err
	}

	p := &Pow{store: store}
	p.mem = inst.GetExport(store, "memory").Memory()
	p.solve = inst.GetExport(store, "wasm_solve").Func()
	p.hash = inst.GetExport(store, "wasm_deepseek_hash_v1").Func()
	p.malloc = inst.GetExport(store, "__wbindgen_export_0").Func()
	p.sp = inst.GetExport(store, "__wbindgen_add_to_stack_pointer").Func()

	if p.mem == nil || p.solve == nil || p.malloc == nil || p.sp == nil {
		return nil, fmt.Errorf("invalid wasm exports")
	}
	return p, nil
}

func (p *Pow) put(s string) (int32, int32, error) {
	b := []byte(s)
	res, err := p.malloc.Call(p.store, len(b), 1)
	if err != nil {
		return 0, 0, err
	}
	ptr := res.(int32)
	p.mem.Write(p.store, b, int(ptr))
	return ptr, int32(len(b)), nil
}

func (p *Pow) SolveChallenge(challenge, salt string, expireAt int64, difficulty int) (int, error) {
	rp, err := p.sp.Call(p.store, int32(-16))
	if err != nil {
		return 0, err
	}
	retPtr := rp.(int32)

	cp, cl, _ := p.put(challenge)
	prefix := fmt.Sprintf("%s_%d_", salt, expireAt)
	pp, pl, _ := p.put(prefix)

	_, err = p.solve.Call(p.store, retPtr, cp, cl, pp, pl, float64(difficulty))
	if err != nil {
		return 0, err
	}

	// Read status and value
	data := p.mem.UnsafeData(p.store)
	status := *(*int32)(unsafe.Pointer(&data[retPtr]))
	val := *(*float64)(unsafe.Pointer(&data[retPtr+8]))
	p.sp.Call(p.store, int32(16))

	if status == 0 {
		return 0, fmt.Errorf("pow solver found no answer")
	}
	return int(val), nil
}

func PowHeader(ch map[string]interface{}, answer int) string {
	d := map[string]interface{}{
		"algorithm":   ch["algorithm"],
		"challenge":   ch["challenge"],
		"salt":        ch["salt"],
		"answer":      answer,
		"signature":   ch["signature"],
		"target_path": ch["target_path"],
	}
	b, _ := json.Marshal(d)
	return base64.StdEncoding.EncodeToString(b)
}
