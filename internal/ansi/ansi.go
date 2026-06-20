// Package ansi strips ANSI terminal escape sequences from captured command
// output, for the surfaces that cannot render them (the outcome-classification
// regex and the GitHub check-run markdown). The live viewer renders ANSI itself,
// so the streamed/stored log is left raw.
package ansi

import "regexp"

// sgrRE matches an ANSI SGR sequence: ESC [ <params> m. terraform (and most
// CLIs) only emit SGR for colour/bold; non-SGR CSI is not produced here.
var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Strip removes SGR sequences, leaving all other bytes (including \n and \r).
func Strip(s string) string { return sgrRE.ReplaceAllString(s, "") }
