package ai

// The OpenAI-compatible client (§12, §17): stdlib net/http speaking the
// chat-completions dialect. It only ever *proposes* — the returned text is a
// suggestion shown to the human, never written anywhere by this package.
// Guardrail (§12): even a prompt-injected model cannot produce anything the
// content contract admits, because every accepted suggestion is later written
// through the same sanitiser as human input.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Kind is a suggestion target (§12, by decreasing value).
type Kind string

const (
	// KindAlt drafts image alt text from the image itself (vision model) —
	// accessibility and SEO in one gesture, the chore everyone skips.
	KindAlt Kind = "alt"
	// KindDescription drafts a meta description from a page's clean fragments —
	// the ideal LLM input.
	KindDescription Kind = "description"
	// KindTitle offers title variants.
	KindTitle Kind = "title"
)

// Request is one suggestion ask. Text carries the page fragments (description,
// title) or the existing alt context; ImageData carries the raw image bytes for
// KindAlt (data-URL-encoded to the vision model).
type Request struct {
	Kind      Kind
	Text      string // page prose / current value, per kind
	ImageData []byte // KindAlt only: the image bytes
	ImageMIME string // KindAlt only: e.g. image/webp
}

// Client is a configured assistant. Obtain one from New; a nil Client means the
// feature is not configured and must not be offered.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client, or nil when the assistant is not configured (§12).
func New(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 60 * time.Second}}
}

// Suggest returns up to a few proposals for the request. It performs no writes;
// the caller shows the proposals and only a human gesture ever commits one.
func (c *Client) Suggest(ctx context.Context, req Request) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("assistant not configured")
	}
	msg, model, err := c.buildMessages(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    msg,
		Temperature: 0.7,
		MaxTokens:   400,
		N:           1,
	})
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(c.cfg.Endpoint, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("assistant request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("assistant returned %s", res.Status)
	}

	var parsed chatResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode assistant response: %w", err)
	}
	return extractProposals(req.Kind, parsed), nil
}

// buildMessages assembles the prompt for a kind and picks the model. The system
// prompt keeps output plain: no markdown, no HTML — the contract admits neither
// in these fields, and the sanitiser would strip it anyway.
func (c *Client) buildMessages(req Request) ([]message, string, error) {
	switch req.Kind {
	case KindAlt:
		if len(req.ImageData) == 0 {
			return nil, "", fmt.Errorf("alt suggestion needs image data")
		}
		mime := req.ImageMIME
		if mime == "" {
			mime = "image/webp"
		}
		dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(req.ImageData)
		return []message{
			{Role: "system", Content: text("Tu rédiges des textes alternatifs d'images pour l'accessibilité. Réponds par une seule description factuelle, concise (moins de 125 caractères), sans guillemets, sans balises, sans phrase d'introduction.")},
			{Role: "user", Content: parts(
				textPart("Décris cette image en texte alternatif :"),
				imagePart(dataURL),
			)},
		}, c.cfg.VisionFor(), nil
	case KindDescription:
		return []message{
			{Role: "system", Content: text("Tu rédiges des méta-descriptions SEO. Réponds par une seule description de 140 à 160 caractères, en texte brut, sans guillemets ni balises, qui résume fidèlement le contenu.")},
			{Role: "user", Content: text("Contenu de la page :\n\n" + clip(req.Text, 6000))},
		}, c.cfg.Model, nil
	case KindTitle:
		return []message{
			{Role: "system", Content: text("Tu proposes des variantes de titre de page, concises et distinctes. Réponds par 3 variantes, une par ligne, en texte brut, sans numérotation ni guillemets.")},
			{Role: "user", Content: text("Contenu de la page :\n\n" + clip(req.Text, 4000))},
		}, c.cfg.Model, nil
	default:
		return nil, "", fmt.Errorf("unknown suggestion kind %q", req.Kind)
	}
}

// extractProposals turns the model's answer into cleaned proposal strings:
// title asks yield several (one per line), the rest a single value. HTML/markup
// is not decoded here — the sanitiser is the authority — but obvious wrapping
// quotes and list bullets are trimmed for a clean preview.
func extractProposals(kind Kind, resp chatResponse) []string {
	if len(resp.Choices) == 0 {
		return nil
	}
	content := strings.TrimSpace(resp.Choices[0].Message.contentText())
	if content == "" {
		return nil
	}
	if kind == KindTitle {
		var out []string
		for _, line := range strings.Split(content, "\n") {
			if p := cleanProposal(line); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if p := cleanProposal(content); p != "" {
		return []string{p}
	}
	return nil
}

func cleanProposal(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-*•0123456789. \t")
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- OpenAI chat-completions wire types -----------------------------------------

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	N           int       `json:"n"`
}

// message.Content is either a plain string or an array of content parts (for
// vision). We marshal whatever text()/parts() produced.
type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func text(s string) any { return s }

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func parts(p ...contentPart) any    { return p }
func textPart(s string) contentPart { return contentPart{Type: "text", Text: s} }
func imagePart(u string) contentPart {
	return contentPart{Type: "image_url", ImageURL: &imageURL{URL: u}}
}

type chatResponse struct {
	Choices []struct {
		Message respMessage `json:"message"`
	} `json:"choices"`
}

type respMessage struct {
	Content json.RawMessage `json:"content"`
}

// contentText decodes an assistant message content that may be a plain string
// or (rarely) an array of text parts.
func (m respMessage) contentText() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var arr []contentPart
	if json.Unmarshal(m.Content, &arr) == nil {
		var b strings.Builder
		for _, p := range arr {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
