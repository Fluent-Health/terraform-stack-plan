// Package links resolves URL templates with {placeholder} substitution.
package links

import "strings"

// Resolve substitutes {key} placeholders in tmpl from vars. If tmpl references
// any key that is missing or empty, Resolve returns "" — callers treat that as
// "no link", so partially-configured runs degrade cleanly. tmpl with no
// placeholders is returned verbatim.
func Resolve(tmpl string, vars map[string]string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		close := strings.IndexByte(tmpl[open:], '}')
		if close < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		close += open
		b.WriteString(tmpl[:open])
		key := tmpl[open+1 : close]
		val, ok := vars[key]
		if !ok || val == "" {
			return ""
		}
		b.WriteString(val)
		tmpl = tmpl[close+1:]
	}
}
