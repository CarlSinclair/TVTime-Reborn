package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"tvt/logger"
)

type Bundle struct {
	langs    map[string]map[string]string
	fallback string
}

func flatten(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, child, out)
		}
	case string:
		out[prefix] = t
	}
}

func Load(fsys fs.FS, dir, fallback string) (*Bundle, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}
	b := &Bundle{langs: map[string]map[string]string{}, fallback: fallback}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", e.Name(), err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", e.Name(), err)
		}
		flat := map[string]string{}
		flatten("", raw, flat)
		b.langs[strings.TrimSuffix(e.Name(), ".json")] = flat
	}
	if _, ok := b.langs[fallback]; !ok {
		return nil, fmt.Errorf("fallback locale %q not found", fallback)
	}
	return b, nil
}

func (b *Bundle) Has(lang string) bool { _, ok := b.langs[lang]; return ok }

func (b *Bundle) HasKey(lang, key string) bool {
	_, ok := b.langs[lang][key]
	if !ok {
		_, ok = b.langs[b.fallback][key]
	}
	return ok
}

// Langs returns the supported language codes in a stable order.

func (b *Bundle) Langs() []string {
	out := make([]string, 0, len(b.langs))
	for k := range b.langs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (b *Bundle) T(lang, key string, args ...any) string {
	s, ok := b.langs[lang][key]
	if !ok {
		s, ok = b.langs[b.fallback][key]
		if !ok {
			logger.Warn("I18N missing key %q lang=%s", key, lang)
			return key
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Pick chooses a supported language from an Accept-Language header.
func (b *Bundle) Pick(header string) string {
	for _, part := range strings.Split(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if tag == "" {
			continue
		}
		if b.Has(tag) {
			return tag
		}
		if base := strings.SplitN(tag, "-", 2)[0]; b.Has(base) {
			return base
		}
	}
	return b.fallback
}

// Prefix returns every key under prefix (with the prefix stripped) for the
// language, falling back per key, for handing to client-side code.
func (b *Bundle) Prefix(lang, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range b.langs[b.fallback] {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	for k, v := range b.langs[lang] {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// TN is T with named {placeholders}, used for stored notifications whose text
// is rendered later in the reader's own language.
func (b *Bundle) TN(lang, key string, vars map[string]string) string {
	s := b.T(lang, key)
	if len(vars) == 0 {
		return s
	}
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
