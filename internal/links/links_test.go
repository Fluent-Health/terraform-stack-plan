package links

import "testing"

func TestResolve(t *testing.T) {
	vars := map[string]string{"sha": "abc1234567", "file": "main.tf", "line": "12"}
	cases := []struct{ name, tmpl, want string }{
		{"all present", "b/{sha}/{file}#L{line}", "b/abc1234567/main.tf#L12"},
		{"literal only", "https://x/y", "https://x/y"},
		{"missing var omits", "b/{sha}/{nope}", ""},
		{"empty var omits", "b/{empty}", ""},
		{"repeated var", "{sha}-{sha}", "abc1234567-abc1234567"},
	}
	vars["empty"] = ""
	for _, c := range cases {
		if got := Resolve(c.tmpl, vars); got != c.want {
			t.Errorf("%s: Resolve(%q) = %q, want %q", c.name, c.tmpl, got, c.want)
		}
	}
}
