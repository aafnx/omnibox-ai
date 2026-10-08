package engine

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyGGUF is a GGUF header of a 32-layer model with an 8k context and a thinking template.
func tinyGGUF() []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	kv := []any{"general.architecture", "llama", "llama.block_count", uint32(32), "llama.context_length", uint32(8192),
		"llama.embedding_length", uint32(4096), "llama.attention.head_count", uint32(32), "llama.attention.head_count_kv", uint32(8),
		"tokenizer.chat_template", "{% if enable_thinking %}<think>{% endif %}"}
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
		}
	}
	return b.Bytes()
}

func TestOllamaModelsAreReadFromDiskWithoutOllama(t *testing.T) {
	dir := t.TempDir()
	blob := func(hex string, data []byte) {
		if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "blobs", "sha256-"+hex), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := func(c string) string { return strings.Repeat(c, 64) }
	blob(h("a"), tinyGGUF())
	blob(h("b"), []byte(`{{ if .Tools }}{{ range .Tools }}{{ .Function }}{{ end }}{{ end }}<think>`))
	blob(h("c"), []byte(`{"model_type":"8.2B","file_type":"Q4_K_M"}`))
	manifest := `{"config":{"digest":"sha256:` + h("c") + `"},"layers":[` +
		`{"mediaType":"application/vnd.ollama.image.model","digest":"sha256:` + h("a") + `","size":5000},` +
		`{"mediaType":"application/vnd.ollama.image.projector","digest":"sha256:` + h("d") + `","size":800},` +
		`{"mediaType":"application/vnd.ollama.image.template","digest":"sha256:` + h("b") + `","size":60}]}`
	write := func(rel, data string) {
		p := filepath.Join(dir, "manifests", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("registry.ollama.ai/library/qwen3/8b", manifest)
	write("hf.co/org/repo-GGUF/Q4_K_M", manifest)
	write("registry.ollama.ai/library/broken/latest", "{")

	got := OllamaDisk(dir)
	if len(got) != 2 || got[0].Name != "hf.co/org/repo-GGUF:Q4_K_M" || got[1].Name != "qwen3:8b" {
		t.Fatalf("models: %+v", got)
	}
	m := got[1]
	if m.Bytes != 5800 || m.Params != "8.2B" || m.Quant != "Q4_K_M" || m.Context != 8192 {
		t.Fatalf("details: %+v", m)
	}
	caps := strings.Join(m.Capabilities, ",")
	if caps != "reasoning,tools,vision" {
		t.Fatalf("capabilities: %s", caps)
	}
	if m.Memory == nil || m.Memory.Weights != 5800 || m.Memory.Layers != 32 || m.Memory.KeyElems != 8*128*32 {
		t.Fatalf("memory: %+v", m.Memory)
	}
	if m.Source != "https://ollama.com/library/qwen3" {
		t.Fatalf("source: %s", m.Source)
	}
	if OllamaDisk(filepath.Join(dir, "missing")) != nil {
		t.Fatal("a missing directory lists nothing")
	}
}
