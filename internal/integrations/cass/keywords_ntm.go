// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/cass/inject.go (in part) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: only ExtractKeywords
// and the helpers it uses (tokenize, removeCodeBlocks, the stop words),
// unchanged; ntm's query, filter and injection code is not taken.

package cass

import (
	"regexp"
	"strings"
	"unicode"
)

// ExtractKeywords extracts meaningful keywords from a prompt.
func ExtractKeywords(prompt string) []string {
	text := strings.ToLower(prompt)
	text = removeCodeBlocks(text)
	words := tokenize(text)

	var keywords []string
	seen := make(map[string]bool)

	for _, word := range words {
		if len(word) < 3 {
			continue
		}
		if isStopWord(word) {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)
	}

	if len(keywords) > 10 {
		keywords = keywords[:10]
	}

	return keywords
}

func tokenize(text string) []string {
	var words []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// Compiled once at package level to avoid repeated compilation in hot paths.
var (
	codeBlockFenceRe  = regexp.MustCompile("(?s)```.*?```")
	codeBlockInlineRe = regexp.MustCompile("`[^`]+`")
)

func removeCodeBlocks(text string) string {
	text = codeBlockFenceRe.ReplaceAllString(text, " ")
	text = codeBlockInlineRe.ReplaceAllString(text, " ")
	return text
}

// stopWords is allocated once at package level to avoid per-call map creation.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"but": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "of": true, "with": true, "by": true, "from": true,
	"as": true, "is": true, "was": true, "are": true, "were": true,
	"been": true, "be": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true,
	"this": true, "that": true, "these": true, "those": true,
	"it": true, "its": true, "they": true, "them": true, "their": true,
	"we": true, "you": true, "your": true, "our": true, "my": true,
	"me": true, "him": true, "her": true, "his": true, "she": true,
	"he": true, "i": true, "all": true, "each": true, "every": true,
	"both": true, "few": true, "more": true, "most": true, "other": true,
	"some": true, "such": true, "no": true, "nor": true, "not": true,
	"only": true, "own": true, "same": true, "so": true, "than": true,
	"too": true, "very": true, "just": true, "also": true, "now": true,
	"can": true, "get": true, "got": true, "how": true, "what": true,
	"when": true, "where": true, "which": true, "who": true, "why": true,
	"new": true, "use": true, "used": true, "using": true,
	"make": true, "made": true, "like": true, "want": true, "need": true,
	"please": true, "help": true, "here": true, "there": true,
	"code": true, "file": true, "function": true, "method": true,
	"class": true, "variable": true, "add": true, "create": true,
	"update": true, "delete": true, "remove": true, "change": true,
	"fix": true, "bug": true, "error": true, "test": true, "write": true,
	"read": true, "run": true, "start": true, "stop": true,
}

func isStopWord(word string) bool {
	return stopWords[word]
}
