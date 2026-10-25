package agenttools

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
)

// secretFlag matches a flag name (without dashes) that looks like it carries a secret.
var secretFlag = regexp.MustCompile(`(?i)token|key|secret|passw(or)?d`)

// MaskURL shows a URL to agents: userinfo and every query value become "•••" (${secret:…}
// references stay), scheme, host, path, query keys and fragment stay. Text that does not parse
// as a URL with a scheme and host is masked as a whole.
func MaskURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return mask
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString(MaskValue(u.User.String()) + "@")
	}
	b.WriteString(u.Host)
	b.WriteString(u.EscapedPath())
	if u.RawQuery != "" || u.ForceQuery {
		b.WriteByte('?')
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			if k, v, ok := strings.Cut(p, "="); ok {
				parts[i] = k + "=" + maskQueryValue(v)
			}
		}
		b.WriteString(strings.Join(parts, "&"))
	}
	if u.Fragment != "" {
		b.WriteString("#" + u.EscapedFragment())
	}
	return b.String()
}

func maskQueryValue(v string) string {
	if dec, err := url.QueryUnescape(v); err == nil && secretRef.MatchString(dec) {
		return MaskValue(dec)
	}
	return MaskValue(v)
}

// MaskArgs shows server arguments to agents: the value of a secret-looking flag ("--api-key=x",
// "--auth-token x", "-secret x", "--password=x") becomes "•••" (${secret:…} references stay), and
// URL arguments are shown like MaskURL. A nil slice stays nil.
func MaskArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := slices.Clone(args)
	for i := 0; i < len(out); i++ {
		a := out[i]
		if isURL(a) {
			out[i] = MaskURL(a)
			continue
		}
		name, ok := flagName(a)
		if !ok || !secretFlag.MatchString(name) {
			continue
		}
		if k, v, has := strings.Cut(a, "="); has {
			out[i] = k + "=" + MaskValue(v)
		} else if i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") {
			i++
			out[i] = MaskValue(out[i])
		}
	}
	return out
}

// flagName is the name of a "-x" or "--x[=v]" argument without dashes and value.
func flagName(a string) (string, bool) {
	if !strings.HasPrefix(a, "-") {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
	return name, name != ""
}

func isURL(s string) bool {
	i := strings.Index(s, "://")
	return i > 0 && !strings.ContainsAny(s[:i], " =")
}

// urlInText finds URLs in free text (error messages).
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// maskText masks free text about the server s (an error message): URLs as MaskURL, then every
// literal secret of s (env and header values, secret-looking argument values, URL userinfo and
// query values) where it appears.
func maskText(text string, s *config.Server) string {
	if text == "" {
		return ""
	}
	text = urlInText.ReplaceAllStringFunc(text, MaskURL)
	if s != nil {
		for _, lit := range secretLiterals(s) {
			text = strings.ReplaceAll(text, lit, mask)
		}
	}
	return text
}

// secretLiterals lists the literal secret values of s, longest first (so a value that contains
// another is replaced whole). Values shorter than 3 bytes are left out: masking them would garble
// the text and they are no meaningful secret.
func secretLiterals(s *config.Server) []string {
	var out []string
	add := func(v string) {
		for _, part := range secretRef.Split(v, -1) {
			if len(part) >= 3 {
				out = append(out, part)
			}
		}
	}
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for _, v := range m {
			add(v)
		}
	}
	masked := MaskArgs(s.Args)
	for i, a := range s.Args {
		if masked[i] != a && !isURL(a) {
			if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
				add(v)
			} else {
				add(a)
			}
		}
	}
	for _, raw := range append(slices.Clone(s.Args), s.URL) {
		if !isURL(raw) {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if u.User != nil {
			add(u.User.String())
			if p, ok := u.User.Password(); ok {
				add(p)
			}
		}
		for _, vs := range u.Query() {
			for _, v := range vs {
				add(v)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(out)
}
