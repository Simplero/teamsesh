// Package secrets finds likely credentials in text before it leaves the machine.
//
// Pattern matching misses things (plain passwords, customer data, text in images), so
// callers must also show a preview and ask before sharing.
package secrets

import (
	"encoding/base64"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Hit struct {
	File   string
	Kind   string
	Line   int
	Masked string
	start  int
	end    int
}

type rule struct {
	kind    string
	pattern *regexp.Regexp
	group   int // the capture group that holds the secret; 0 = whole match
	entropy bool
}

var rules = []rule{
	{kind: "private key", pattern: regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END[A-Z ]*PRIVATE KEY-----|$)`)},
	{kind: "GitHub token", pattern: regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})`)},
	{kind: "Anthropic key", pattern: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{kind: "OpenAI key", pattern: regexp.MustCompile(`\bsk-(proj-|svcacct-)?[A-Za-z0-9_\-]{32,}`)},
	{kind: "AWS key", pattern: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{kind: "Slack token", pattern: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`)},
	{kind: "Stripe key", pattern: regexp.MustCompile(`\b(sk|rk)_live_[A-Za-z0-9]{16,}`)},
	{kind: "Google API key", pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`)},
	{kind: "JWT", pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{kind: "Authorization header", pattern: regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?(?:bearer|basic|token)\s+([A-Za-z0-9._~+/=\-]{16,})`), group: 1},
	{kind: "credential", pattern: regexp.MustCompile(`(?i)(?:api[_-]?key|secret|token|passw(?:or)?d|pwd|auth)[A-Za-z_]*\\?["']?\s*[:=]\s*\\?["']?([^\s"'\\,;{}()<>&]{12,})`), group: 1, entropy: true},
	{kind: "credential in URL", pattern: regexp.MustCompile(`[a-z][a-z0-9+.\-]*://[^\s/:@"']+:([^\s/@"']{6,})@`), group: 1},
}

var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)

// Scan returns every hit in text, in order.
func Scan(file, text string) []Hit {
	var hits []Hit
	for _, r := range rules {
		for _, m := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := m[0], m[1]
			if r.group > 0 && len(m) > 2*r.group+1 && m[2*r.group] >= 0 {
				start, end = m[2*r.group], m[2*r.group+1]
			}
			value := text[start:end]
			if r.entropy && (entropy(value) < 3.5 || looksLikeCode(value)) {
				continue
			}
			hits = append(hits, Hit{File: file, Kind: r.kind, Line: 1 + strings.Count(text[:start], "\n"), Masked: mask(value), start: start, end: end})
		}
	}
	// Secrets hidden in base64: decode long runs and look again with the specific rules.
	for _, m := range base64Run.FindAllStringIndex(text, -1) {
		decoded, err := base64.StdEncoding.DecodeString(padded(text[m[0]:m[1]]))
		if err != nil || !printable(decoded) {
			continue
		}
		for _, r := range rules {
			if r.entropy {
				continue
			}
			if r.pattern.Match(decoded) {
				hits = append(hits, Hit{File: file, Kind: r.kind + " (base64)", Line: 1 + strings.Count(text[:m[0]], "\n"), Masked: mask(text[m[0]:m[1]]), start: m[0], end: m[1]})
				break
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
	return dedupe(hits)
}

// Redact replaces every hit with [REDACTED]. The result stays valid JSON when the input was,
// because every secret sits inside a JSON string.
func Redact(text string, hits []Hit) string {
	var b strings.Builder
	last := 0
	for _, h := range hits {
		if h.start < last {
			continue
		}
		b.WriteString(text[last:h.start])
		b.WriteString("[REDACTED]")
		last = h.end
	}
	b.WriteString(text[last:])
	return b.String()
}

func dedupe(hits []Hit) []Hit {
	var out []Hit
	end := -1
	for _, h := range hits {
		if h.start < end {
			continue
		}
		out = append(out, h)
		end = h.end
	}
	return out
}

func mask(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-2:] + " (" + strconv.Itoa(len(s)) + " chars)"
}

func entropy(s string) float64 {
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	var e float64
	n := float64(len(s))
	for _, c := range counts {
		p := float64(c) / n
		e -= p * math.Log2(p)
	}
	return e
}

// looksLikeCode rejects values that are paths, method calls, symbols or variable names,
// which fill coding transcripts ("token = user.auth_token", "auth: :API_KEY").
func looksLikeCode(s string) bool {
	if strings.Count(s, "/") >= 2 || strings.ContainsAny(s, ".[]:$@") {
		return true
	}
	digits := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits < 2
}

func padded(s string) string {
	for len(s)%4 != 0 {
		s += "="
	}
	return s
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 9 || (c > 13 && c < 32) || c > 126 {
			return false
		}
	}
	return true
}
