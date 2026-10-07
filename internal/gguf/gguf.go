// Package gguf reads the metadata of a GGUF model file (layers, heads, context) and estimates
// where the model fits: VRAM, RAM or both, for a given context and KV cache type. It reads only
// the header, never the weights.
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Meta is what the estimate needs from the header.
type Meta struct {
	Arch        string `json:"arch"`
	Blocks      int    `json:"blocks"`
	Context     int    `json:"context_length"`
	Embedding   int    `json:"embedding_length"`
	Heads       int    `json:"head_count"`
	HeadsKV     int    `json:"head_count_kv"`
	KeyLength   int    `json:"key_length"`
	ValueLength int    `json:"value_length"`
	Experts     int    `json:"expert_count"`
}

// Header limits: a damaged or hostile file cannot make the reader allocate much.
const (
	maxKeys   = 1 << 16
	maxString = 1 << 20
	maxArray  = 1 << 24
)

var errFormat = errors.New("файл не похож на GGUF")

type reader struct {
	r *bufio.Reader
}

func (r *reader) u32() (uint32, error) {
	var v uint32
	return v, binary.Read(r.r, binary.LittleEndian, &v)
}

func (r *reader) u64() (uint64, error) {
	var v uint64
	return v, binary.Read(r.r, binary.LittleEndian, &v)
}

func (r *reader) str() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	if n > maxString {
		return "", errFormat
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r.r, b)
	return string(b), err
}

var sizes = map[uint32]int{0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 6: 4, 7: 1, 10: 8, 11: 8, 12: 8}

// value reads one value; numbers come back as int64 (or the largest of an array), the rest is
// skipped.
func (r *reader) value(kind uint32) (int64, string, error) {
	switch kind {
	case 8:
		s, err := r.str()
		return 0, s, err
	case 9:
		inner, err := r.u32()
		if err != nil {
			return 0, "", err
		}
		n, err := r.u64()
		if err != nil || n > maxArray {
			return 0, "", errFormat
		}
		var best int64
		for range n {
			v, _, err := r.value(inner)
			if err != nil {
				return 0, "", err
			}
			best = max(best, v)
		}
		return best, "", nil
	}
	size, ok := sizes[kind]
	if !ok {
		return 0, "", errFormat
	}
	var b [8]byte
	if _, err := io.ReadFull(r.r, b[:size]); err != nil {
		return 0, "", err
	}
	switch kind {
	case 0, 7:
		return int64(b[0]), "", nil
	case 1:
		return int64(int8(b[0])), "", nil
	case 2:
		return int64(binary.LittleEndian.Uint16(b[:])), "", nil
	case 3:
		return int64(int16(binary.LittleEndian.Uint16(b[:]))), "", nil
	case 4:
		return int64(binary.LittleEndian.Uint32(b[:])), "", nil
	case 5:
		return int64(int32(binary.LittleEndian.Uint32(b[:]))), "", nil
	case 6:
		return int64(math.Float32frombits(binary.LittleEndian.Uint32(b[:]))), "", nil
	case 12:
		return int64(math.Float64frombits(binary.LittleEndian.Uint64(b[:]))), "", nil
	}
	return int64(binary.LittleEndian.Uint64(b[:])), "", nil
}

// Read parses the header of a GGUF file (the first part of a split model).
func Read(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	return parse(f)
}

func parse(in io.Reader) (Meta, error) {
	r := &reader{r: bufio.NewReaderSize(in, 1<<16)}
	var magic [4]byte
	if _, err := io.ReadFull(r.r, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return Meta{}, errFormat
	}
	version, err := r.u32()
	if err != nil || version < 2 {
		return Meta{}, fmt.Errorf("GGUF версии %d не поддерживается", version)
	}
	if _, err := r.u64(); err != nil { // tensor count
		return Meta{}, errFormat
	}
	count, err := r.u64()
	if err != nil || count > maxKeys {
		return Meta{}, errFormat
	}
	numbers := map[string]int64{}
	var m Meta
	for range count {
		key, err := r.str()
		if err != nil {
			return Meta{}, errFormat
		}
		kind, err := r.u32()
		if err != nil {
			return Meta{}, errFormat
		}
		n, s, err := r.value(kind)
		if err != nil {
			return Meta{}, errFormat
		}
		if key == "general.architecture" {
			m.Arch = s
		}
		numbers[key] = n
	}
	if m.Arch == "" {
		return Meta{}, errors.New("в GGUF нет general.architecture")
	}
	get := func(name string) int { return int(numbers[m.Arch+"."+name]) }
	m.Blocks, m.Context, m.Embedding = get("block_count"), get("context_length"), get("embedding_length")
	m.Heads, m.HeadsKV = get("attention.head_count"), get("attention.head_count_kv")
	m.KeyLength, m.ValueLength, m.Experts = get("attention.key_length"), get("attention.value_length"), get("expert_count")
	if m.HeadsKV == 0 {
		m.HeadsKV = m.Heads
	}
	if m.KeyLength == 0 && m.Heads > 0 {
		m.KeyLength = m.Embedding / m.Heads
	}
	if m.ValueLength == 0 {
		m.ValueLength = m.KeyLength
	}
	if m.Blocks <= 0 {
		return Meta{}, errors.New("в GGUF нет числа слоёв")
	}
	return m, nil
}

// cacheBytes is the size of one cached value by KV cache type (llama.cpp block formats).
func cacheBytes(kind string) float64 {
	switch strings.ToLower(kind) {
	case "f32":
		return 4
	case "q8_0":
		return 34.0 / 32
	case "q5_1":
		return 24.0 / 32
	case "q5_0":
		return 22.0 / 32
	case "q4_1":
		return 20.0 / 32
	case "q4_0", "iq4_nl":
		return 18.0 / 32
	}
	return 2 // f16, bf16
}
