package ansi

import "testing"

func TestStrip(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "no codes here", "no codes here"},
		{"sgr", "\x1b[1m\x1b[32mApply complete!\x1b[0m", "Apply complete!"},
		{"interleaved", "0 \x1b[31madded\x1b[0m, 2 changed", "0 added, 2 changed"},
		{"keeps newlines+cr", "a\x1b[0m\r\nb\n", "a\r\nb\n"},
		{"apply summary", "\x1b[1mApply complete! Resources: 0 added, 0 changed, 0 destroyed.\x1b[0m",
			"Apply complete! Resources: 0 added, 0 changed, 0 destroyed."},
	}
	for _, c := range cases {
		if got := Strip(c.in); got != c.want {
			t.Errorf("%s: Strip(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
