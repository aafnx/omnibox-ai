package gguf

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// header writes a minimal GGUF header with the given metadata.
func header(kv []any) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(len(kv) / 2))
	for i := 0; i < len(kv); i += 2 {
		str(kv[i].(string))
		switch v := kv[i+1].(type) {
		case string:
			w(uint32(8))
			str(v)
		case uint32:
			w(uint32(4))
			w(v)
		case []string:
			w(uint32(9))
			w(uint32(8))
			w(uint64(len(v)))
			for _, s := range v {
				str(s)
			}
		case []int32:
			w(uint32(9))
			w(uint32(5))
			w(uint64(len(v)))
			for _, n := range v {
				w(n)
			}
		}
	}
	return b.Bytes()
}

func llama(blocks uint32) []byte {
	return header([]any{
		"general.architecture", "llama",
		"tokenizer.ggml.tokens", []string{"<s>", "</s>", "привет"},
		"llama.block_count", blocks,
		"llama.context_length", uint32(131072),
		"llama.embedding_length", uint32(5120),
		"llama.attention.head_count", uint32(40),
		"llama.attention.head_count_kv", []int32{8, 8, 16},
	})
}

func TestReadHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m-Q4_K_M.gguf")
	os.WriteFile(path, append(llama(64), make([]byte, 100)...), 0o644)
	m, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Arch != "llama" || m.Blocks != 64 || m.Context != 131072 || m.HeadsKV != 16 || m.KeyLength != 128 || m.ValueLength != 128 {
		t.Fatalf("%+v", m)
	}
	for _, bad := range [][]byte{[]byte("GGML"), llama(64)[:30], header([]any{"llama.block_count", uint32(4)})} {
		if _, err := parse(bytes.NewReader(bad)); err == nil {
			t.Fatalf("accepted %q", bad[:4])
		}
	}
}

func TestPlaceRecommendsLayers(t *testing.T) {
	m := Meta{Arch: "llama", Blocks: 64, Context: 131072, HeadsKV: 8, KeyLength: 128, ValueLength: 128}
	const gb = int64(1 << 30)
	// A 27B Q4 model with 32k context does not fit 16 GB: about half of the layers go to the GPU.
	e := Place(m, 16*gb, 32768, "f16", "f16", 16*gb, 32*gb)
	if e.KVCache != 8*gb || e.Placement != "hybrid" || e.GPULayers <= 0 || e.GPULayers >= 64 || e.VRAMNeeded > 16*gb {
		t.Fatalf("%+v", e)
	}
	// A q8_0 cache is about half of f16.
	if q := Place(m, 16*gb, 32768, "q8_0", "q8_0", 16*gb, 32*gb); q.KVCache >= e.KVCache*6/10 || q.GPULayers <= e.GPULayers {
		t.Fatalf("q8_0: %+v", q)
	}
	// A small model fits entirely.
	if s := Place(Meta{Blocks: 32, HeadsKV: 8, KeyLength: 128, ValueLength: 128, Context: 32768}, 5*gb, 8192, "f16", "f16", 16*gb, 32*gb); s.Placement != "gpu" || s.GPULayers != 32 {
		t.Fatalf("%+v", s)
	}
	// Without a GPU everything is in RAM; too big for RAM is said plainly.
	if c := Place(m, 16*gb, 8192, "f16", "f16", 0, 32*gb); c.Placement != "cpu" {
		t.Fatalf("%+v", c)
	}
	if n := Place(m, 40*gb, 131072, "f16", "f16", 16*gb, 32*gb); n.Placement != "none" || !strings.Contains(n.Text, "Не помещается") {
		t.Fatalf("%+v", n)
	}
	if over := Place(m, 5*gb, 262144, "q4_0", "q4_0", 16*gb, 64*gb); !strings.Contains(over.Text, "больше обученного") {
		t.Fatal(over.Text)
	}
}

func TestHybridAttentionKeepsKVInFewLayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qwen.gguf")
	os.WriteFile(path, header([]any{
		"general.architecture", "qwen35",
		"qwen35.block_count", uint32(64),
		"qwen35.full_attention_interval", uint32(4),
		"qwen35.attention.head_count", uint32(24),
		"qwen35.attention.head_count_kv", uint32(4),
		"qwen35.attention.key_length", uint32(256),
		"qwen35.attention.value_length", uint32(256),
	}), 0o644)
	m, err := Read(path)
	if err != nil || m.KVLayers != 16 {
		t.Fatalf("%+v %v", m, err)
	}
	const gb = int64(1 << 30)
	full := m
	full.KVLayers = 0
	if h, f := Place(m, 10*gb, 65536, "q8_0", "q8_0", 16*gb, 32*gb), Place(full, 10*gb, 65536, "q8_0", "q8_0", 16*gb, 32*gb); h.KVCache*4 != f.KVCache {
		t.Fatalf("hybrid %d, full %d", h.KVCache, f.KVCache)
	}
}

func TestChatTemplateFeatures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.gguf")
	os.WriteFile(path, header([]any{
		"general.architecture", "qwen3", "general.name", "Qwen3 8B", "general.size_label", "8B",
		"qwen3.block_count", uint32(36),
		"tokenizer.chat_template", "{%- if tools %}<tool_call>{{ tools }}</tool_call>{% endif %}{%- if enable_thinking %}<think>{% endif %}",
	}), 0o644)
	m, err := Read(path)
	if err != nil || !m.Thinking || !m.Tools || m.Name != "Qwen3 8B" || m.SizeLabel != "8B" {
		t.Fatalf("%+v %v", m, err)
	}
	if th, tl := templateFeatures("{{ messages }}"); th || tl {
		t.Fatal("a plain template claims features")
	}
}
