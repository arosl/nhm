// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/codeblock/parser.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the package (nhm's
// panediff); only what ntm diff --code-only uses: CodeBlock's language,
// content and lines, the language names, and the fence parser, which is
// ExtractFromText itself (ntm's Parser.Parse, without the Parser type and
// its language filter), with its comments on the fences shortened. The
// file path detection (FilePath, IsNew, SourcePane), Extraction and
// DetectLanguage are left out.

package panediff

import "strings"

// CodeBlock represents a single code block extracted from markdown.
type CodeBlock struct {
	Language  string `json:"language"`   // From fence (python, bash, etc.)
	Content   string `json:"content"`    // The code itself
	StartLine int    `json:"start_line"` // Line number in source
	EndLine   int    `json:"end_line"`   // Ending line number
}

// ExtractFromText extracts the fenced code blocks of text, in order.
func ExtractFromText(text string) []CodeBlock {
	var blocks []CodeBlock
	lines := strings.Split(text, "\n")

	inBlock := false
	fenceLen := 0
	var currentLang string
	var currentContent strings.Builder
	var startLine int

	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r") // Handle CRLF

		// A fence line starts at the start of the line with at least 3
		// backticks.
		isFence := false
		fl := 0
		if strings.HasPrefix(trimmed, "```") {
			// Count backticks
			for j := 0; j < len(trimmed); j++ {
				if trimmed[j] == '`' {
					fl++
				} else {
					break
				}
			}
			isFence = fl >= 3
		}

		if isFence {
			if !inBlock {
				// Start of block
				inBlock = true
				fenceLen = fl
				startLine = i + 1 // 1-based
				// Language is the rest of the line after backticks
				currentLang = strings.TrimSpace(trimmed[fl:])
				currentContent.Reset()
			} else {
				// A closing fence must be at least as long as the opening
				// fence.
				if fl >= fenceLen {
					// Close block
					inBlock = false
					content := currentContent.String()
					// Remove trailing newline added by loop
					content = strings.TrimSuffix(content, "\n")
					blocks = append(blocks, CodeBlock{
						Language:  normalizeLanguage(currentLang),
						Content:   content,
						StartLine: startLine,
						EndLine:   i + 1,
					})
				} else {
					// Fence too short, treat as content
					currentContent.WriteString(trimmed)
					currentContent.WriteString("\n")
				}
			}
		} else {
			if inBlock {
				currentContent.WriteString(trimmed)
				currentContent.WriteString("\n")
			}
		}
	}

	return blocks
}

// languageMap maps aliases to canonical language names
var languageMap = map[string]string{
	"js": "javascript", "jsx": "javascript", "javascript": "javascript",
	"ts": "typescript", "tsx": "typescript", "typescript": "typescript",
	"py": "python", "python": "python",
	"rb": "ruby", "ruby": "ruby",
	"sh": "bash", "shell": "bash", "zsh": "bash", "bash": "bash",
	"yml": "yaml", "yaml": "yaml",
	"dockerfile": "docker", "docker": "docker",
	"md": "markdown", "markdown": "markdown",
	"c++": "cpp", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp",
	"c": "c", "h": "c",
	"cs": "csharp", "csharp": "csharp",
	"rs": "rust", "rust": "rust",
	"kt": "kotlin", "kotlin": "kotlin",
	"go":   "go",
	"java": "java",
	"php":  "php",
	"html": "html",
	"css":  "css",
	"scss": "scss", "sass": "scss",
	"less": "less",
	"sql":  "sql",
	"json": "json",
	"xml":  "xml",
	"toml": "toml",
	"lua":  "lua",
	"pl":   "perl", "perl": "perl",
	"r":     "r",
	"swift": "swift",
	"scala": "scala",
	"el":    "elisp", "lisp": "lisp",
	"clj": "clojure",
	"tf":  "terraform",
	"vim": "vim",
}

// normalizeLanguage normalizes language names.
func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return "text"
	}
	if canonical, ok := languageMap[lang]; ok {
		return canonical
	}
	return lang
}
