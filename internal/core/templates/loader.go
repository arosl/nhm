package templates

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Loader finds a template by key (plan-v8 2.3, 2.4). The order, first key
// wins:
//
//  1. <project>/.nhm/templates/<key>.md
//  2. $XDG_CONFIG_HOME/nhm/templates/<key>.md (~/.config/nhm/templates)
//  3. the palette catalogs: <project>/command_palette.md,
//     $XDG_CONFIG_HOME/nhm/command_palette.md, ACFS's catalog
//  4. ntm's builtin templates
//
// A key that names an existing .md file is that file.
type Loader struct {
	// ProjectDir and UserDir hold <key>.md templates; "" skips one.
	ProjectDir string
	UserDir    string
	Catalogs   []string
}

// AcfsCatalog is ACFS's palette catalog on an installed machine.
const AcfsCatalog = ".acfs/onboard/docs/ntm/command_palette.md"

// NewLoader returns the loader for a project root ("" for none).
func NewLoader(projectRoot string) *Loader {
	config := os.Getenv("XDG_CONFIG_HOME")
	home, _ := os.UserHomeDir()
	if config == "" && home != "" {
		config = filepath.Join(home, ".config")
	}
	l := &Loader{}
	if projectRoot != "" {
		l.ProjectDir = filepath.Join(projectRoot, ".nhm", "templates")
	}
	if config != "" {
		l.UserDir = filepath.Join(config, "nhm", "templates")
	}
	if projectRoot != "" {
		l.Catalogs = append(l.Catalogs, filepath.Join(projectRoot, "command_palette.md"))
	}
	if config != "" {
		l.Catalogs = append(l.Catalogs, filepath.Join(config, "nhm", "command_palette.md"))
	}
	if home != "" {
		l.Catalogs = append(l.Catalogs, filepath.Join(home, AcfsCatalog))
	}
	return l
}

// TemplateNotFoundError means no source has the key.
type TemplateNotFoundError struct {
	Name string
}

func (e *TemplateNotFoundError) Error() string {
	return fmt.Sprintf("template not found: %s", e.Name)
}

// keyPattern is a template or palette key: no path separators, no dots
// at the start, so a key never leaves its directory.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// Load finds a template by key, or reads the .md file a key names.
func (l *Loader) Load(key string) (*Template, error) {
	if strings.HasSuffix(key, ".md") && strings.ContainsRune(key, filepath.Separator) {
		return loadFile(key, SourceProject, strings.TrimSuffix(filepath.Base(key), ".md"))
	}
	name := strings.TrimSuffix(key, ".md")
	if !keyPattern.MatchString(name) {
		return nil, fmt.Errorf("invalid template key %q", key)
	}
	for _, d := range []struct {
		dir    string
		source TemplateSource
	}{{l.ProjectDir, SourceProject}, {l.UserDir, SourceUser}} {
		if d.dir == "" {
			continue
		}
		t, err := loadFile(filepath.Join(d.dir, name+".md"), d.source, name)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for _, path := range l.Catalogs {
		entries, err := ReadCatalog(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Key == name {
				return &Template{Name: e.Key, Description: e.Label, Body: e.Body, Source: SourceUser, SourcePath: path}, nil
			}
		}
	}
	if t := GetBuiltin(name); t != nil {
		return t, nil
	}
	return nil, &TemplateNotFoundError{Name: name}
}

func loadFile(path string, source TemplateSource, name string) (*Template, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", path, err)
	}
	if t.Name == "" {
		t.Name = name
	}
	t.Source, t.SourcePath = source, path
	return t, nil
}

// CatalogEntry is one prompt of a palette catalog.
type CatalogEntry struct {
	Category string
	Key      string
	Label    string
	Body     string
}

// ReadCatalog reads a palette catalog: "## Category" lines, and for each
// prompt a "### key | Label" line followed by its text up to the next
// heading. Lines before the first "##" are the file's comments.
func ReadCatalog(path string) ([]CatalogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []CatalogEntry
	var cur *CatalogEntry
	var body []string
	category := ""
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	started := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "### "):
			flush()
			key, label, _ := strings.Cut(strings.TrimPrefix(line, "### "), "|")
			key = strings.TrimSpace(key)
			if !keyPattern.MatchString(key) {
				return nil, fmt.Errorf("%s: invalid palette key %q", path, key)
			}
			cur = &CatalogEntry{Category: category, Key: key, Label: strings.TrimSpace(label)}
		case strings.HasPrefix(line, "## "):
			flush()
			category, started = strings.TrimSpace(strings.TrimPrefix(line, "## ")), true
		case cur != nil:
			body = append(body, line)
		case !started:
			// The file's leading comments.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	flush()
	return out, nil
}
