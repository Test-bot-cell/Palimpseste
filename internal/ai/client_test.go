package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockProvider stands in for an OpenAI-compatible endpoint, capturing the
// request and returning a scripted completion.
func mockProvider(t *testing.T, reply string, capture *chatRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, capture)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": reply}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestNotConfiguredIsNil(t *testing.T) {
	if New(Config{}) != nil {
		t.Error("empty config should yield a nil client (§12: the feature does not exist)")
	}
	if New(Config{Endpoint: "http://x"}) != nil {
		t.Error("endpoint without a model is not configured")
	}
	c := New(Config{Endpoint: "http://x/v1", Model: "m"})
	if c == nil {
		t.Error("a complete config should yield a client")
	}
}

func TestSuggestDescription(t *testing.T) {
	var captured chatRequest
	ts := mockProvider(t, "Une description SEO fidèle et concise du contenu de la page.", &captured)
	defer ts.Close()

	c := New(Config{Endpoint: ts.URL + "/v1", Model: "gpt-test", APIKey: "k"})
	got, err := c.Suggest(context.Background(), Request{Kind: KindDescription, Text: "Drake OS est une distribution calme."})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "description SEO") {
		t.Errorf("proposals = %v", got)
	}
	if captured.Model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", captured.Model)
	}
}

func TestSuggestTitleSplitsLines(t *testing.T) {
	ts := mockProvider(t, "1. Premier titre\n2. Deuxième titre\n- Troisième titre", nil)
	defer ts.Close()
	c := New(Config{Endpoint: ts.URL + "/v1", Model: "m"})
	got, err := c.Suggest(context.Background(), Request{Kind: KindTitle, Text: "contenu"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("titles = %v, want 3", got)
	}
	for _, g := range got {
		if strings.HasPrefix(g, "1.") || strings.HasPrefix(g, "-") || strings.HasPrefix(g, `"`) {
			t.Errorf("proposal not cleaned: %q", g)
		}
	}
}

func TestSuggestAltSendsImage(t *testing.T) {
	var captured chatRequest
	ts := mockProvider(t, "Un bureau Linux au premier démarrage.", &captured)
	defer ts.Close()
	c := New(Config{Endpoint: ts.URL + "/v1", Model: "m", VisionModel: "vision"})
	got, err := c.Suggest(context.Background(), Request{Kind: KindAlt, ImageData: []byte("RIFFwebpbytes"), ImageMIME: "image/webp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("alt = %v", got)
	}
	if captured.Model != "vision" {
		t.Errorf("alt used model %q, want the vision model", captured.Model)
	}
	// The image must have been sent as a data URL content part.
	raw, _ := json.Marshal(captured.Messages)
	if !strings.Contains(string(raw), "data:image/webp;base64,") {
		t.Errorf("image not sent as a data URL:\n%s", raw)
	}
}

func TestAltWithoutImageErrors(t *testing.T) {
	c := New(Config{Endpoint: "http://x/v1", Model: "m"})
	if _, err := c.Suggest(context.Background(), Request{Kind: KindAlt}); err == nil {
		t.Error("alt suggestion without image data should error")
	}
}

// The config parser: env wins, config.toml fills gaps, [ai] table only.
func TestConfigLoadEnvAndFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := writeConfig(dir, `
[other]
endpoint = "ignored"

[ai]
endpoint = "http://localhost:11434/v1"
model = "llama3"
vision_model = "llava"
`); err != nil {
		t.Fatal(err)
	}

	// Env overrides the file's endpoint, file supplies the models.
	t.Setenv("PALIMPSESTE_AI_ENDPOINT", "https://api.openai.com/v1")
	t.Setenv("PALIMPSESTE_AI_KEY", "")
	t.Setenv("PALIMPSESTE_AI_MODEL", "")
	t.Setenv("PALIMPSESTE_AI_VISION_MODEL", "")

	cfg := Load()
	if cfg.Endpoint != "https://api.openai.com/v1" {
		t.Errorf("endpoint = %q, env should win", cfg.Endpoint)
	}
	if cfg.Model != "llama3" || cfg.VisionModel != "llava" {
		t.Errorf("models not read from [ai]: %+v", cfg)
	}
	if !cfg.Configured() {
		t.Error("should be configured")
	}
}

func TestConfigOllamaLocal(t *testing.T) {
	// The same client covers local Ollama: endpoint set, no key needed.
	cfg := Config{Endpoint: "http://localhost:11434/v1", Model: "llama3"}
	if !cfg.Configured() {
		t.Error("local Ollama config should be Configured without a key")
	}
	if New(cfg) == nil {
		t.Error("local Ollama should yield a client")
	}
}

// writeConfig writes a config.toml under an XDG config dir for the loader test.
func writeConfig(xdgDir, body string) error {
	dir := filepath.Join(xdgDir, "palimpseste")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644)
}
