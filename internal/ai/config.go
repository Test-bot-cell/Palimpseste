// Package ai is the optional edit-time assistant (§12). Its inviolable rule:
// never in the build, only while editing. An AI in the pipeline would kill
// reproducibility; an AI in the overlay is merely a typing aid — it proposes,
// the human validates, the ordinary commit follows, materialisation stays pure.
//
// The shape is deliberately thin (§12, §17): no model, no inference runtime in
// the binary — a plain HTTP client speaking the OpenAI-compatible dialect, with
// endpoint and key configured outside the repository (env or config.toml). That
// one client covers remote APIs and Ollama running locally (localhost:11434 —
// nothing leaves the machine). Unconfigured, the feature does not exist.
package ai

import (
	"os"
	"path/filepath"
	"strings"
)

// Config is the resolved provider configuration. Zero value = not configured.
type Config struct {
	Endpoint    string // OpenAI-compatible base, e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	APIKey      string // bearer token; empty is valid (local Ollama needs none)
	Model       string // text model for descriptions/titles
	VisionModel string // vision model for image alt text; falls back to Model
}

// Configured reports whether the assistant exists at all (§12: "Non configuré,
// la fonctionnalité n'existe pas").
func (c Config) Configured() bool { return c.Endpoint != "" && c.Model != "" }

// VisionFor returns the model to use for a vision request: the dedicated one if
// set, else the text model (which may itself be multimodal).
func (c Config) VisionFor() string {
	if c.VisionModel != "" {
		return c.VisionModel
	}
	return c.Model
}

// Load resolves configuration from the environment first, then fills any gaps
// from $XDG_CONFIG_HOME/palimpseste/config.toml (§3.1) — always outside the
// repository, never site.json. Secrets therefore never enter git.
func Load() Config {
	c := Config{
		Endpoint:    strings.TrimSpace(os.Getenv("PALIMPSESTE_AI_ENDPOINT")),
		APIKey:      strings.TrimSpace(os.Getenv("PALIMPSESTE_AI_KEY")),
		Model:       strings.TrimSpace(os.Getenv("PALIMPSESTE_AI_MODEL")),
		VisionModel: strings.TrimSpace(os.Getenv("PALIMPSESTE_AI_VISION_MODEL")),
	}
	fillFromConfigFile(&c)
	return c
}

// fillFromConfigFile reads the [ai] table of the config file, setting only keys
// the environment did not already provide. The parser is a deliberately tiny
// TOML subset — key = "value" lines under [ai] — so config carries no
// dependency and no surprises.
func fillFromConfigFile(c *Config) {
	path := configPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	vals := parseAISection(string(raw))
	setIfEmpty(&c.Endpoint, vals["endpoint"])
	setIfEmpty(&c.APIKey, vals["key"])
	setIfEmpty(&c.Model, vals["model"])
	setIfEmpty(&c.VisionModel, vals["vision_model"])
}

func configPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "palimpseste", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "palimpseste", "config.toml")
	}
	return ""
}

// parseAISection extracts key/value pairs from the [ai] table only.
func parseAISection(toml string) map[string]string {
	out := map[string]string{}
	inAI := false
	for _, line := range strings.Split(toml, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inAI = line == "[ai]"
			continue
		}
		if !inAI {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" && v != "" {
		*dst = v
	}
}
