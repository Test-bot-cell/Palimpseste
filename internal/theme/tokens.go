package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The §6 contract for live theming: design tokens are CSS custom properties
// that live at runtime, persisted in a single stylesheet — tokens.css — that
// the editor rewrites and nothing else touches. Re-theming rewrites this one
// file; content and pages stay untouched.

// TokensFile returns the path of the theme's tokens stylesheet: the entry in
// the manifest's ordered styles list whose base name is tokens.css. A theme
// without one simply has no live-editable tokens (the panel disables itself).
func (t *Theme) TokensFile() (string, bool) {
	for _, rel := range t.Styles {
		if strings.EqualFold(filepath.Base(filepath.FromSlash(rel)), "tokens.css") {
			return filepath.Join(t.dir, filepath.FromSlash(rel)), true
		}
	}
	return "", false
}

// ReadTokenValues parses tokens.css and returns the current value of every
// custom property declared in it. The parser is deliberately small: it reads
// the `--name: value;` pairs of a tokens file (comments stripped, parentheses
// respected) — the same shape WriteTokenValues emits.
func (t *Theme) ReadTokenValues() (map[string]string, error) {
	path, ok := t.TokensFile()
	if !ok {
		return map[string]string{}, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tokens.css: %w", err)
	}
	return parseCustomProps(string(raw)), nil
}

// WriteTokenValues rewrites tokens.css with the given values — sorted, one
// declaration per line, atomically — after checking every key is a token the
// manifest declares. This is the only file the token panel ever touches (§6).
func (t *Theme) WriteTokenValues(values map[string]string) (string, error) {
	path, ok := t.TokensFile()
	if !ok {
		return "", fmt.Errorf("theme %q declares no tokens.css in its styles", t.Name)
	}
	for k := range values {
		if _, declared := t.Tokens[k]; !declared {
			return "", fmt.Errorf("token %q is not declared by theme %q", k, t.Name)
		}
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("/* tokens.css — réécrit par le panneau de thème (§6). */\n:root {\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %s;\n", k, values[k])
	}
	b.WriteString("}\n")

	tmp, err := os.CreateTemp(filepath.Dir(path), ".pal-tokens-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}

// parseCustomProps extracts --name: value pairs from CSS text.
func parseCustomProps(css string) map[string]string {
	out := map[string]string{}
	s := stripComments(css)
	for i := 0; i < len(s)-1; i++ {
		if s[i] != '-' || s[i+1] != '-' {
			continue
		}
		// property name: -- then ident chars
		j := i + 2
		for j < len(s) && (isIdentChar(s[j])) {
			j++
		}
		name := s[i:j]
		k := j
		for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n') {
			k++
		}
		if k >= len(s) || s[k] != ':' {
			i = j
			continue
		}
		k++
		depth := 0
		start := k
		for k < len(s) {
			switch s[k] {
			case '(':
				depth++
			case ')':
				depth--
			case ';', '}':
				if depth == 0 {
					goto done
				}
			}
			k++
		}
	done:
		out[name] = strings.TrimSpace(s[start:k])
		i = k
	}
	return out
}

func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isIdentChar(c byte) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
