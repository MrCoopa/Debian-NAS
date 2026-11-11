package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"strings"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// GetFS returns the fs.FS pointing to the static assets
func GetFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}

func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case uint:
		return float64(n)
	default:
		return 0
	}
}

func toUint64(v interface{}) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case uint:
		return uint64(n)
	case int64:
		if n > 0 {
			return uint64(n)
		}
		return 0
	case int:
		if n > 0 {
			return uint64(n)
		}
		return 0
	case float64:
		if n > 0 {
			return uint64(n)
		}
		return 0
	default:
		return 0
	}
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"formatBytes": func(v interface{}) string {
			b := toUint64(v)
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := uint64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
		},
		"formatRate": func(v interface{}) string {
			bps := toFloat64(v)
			if bps < 1024 {
				return fmt.Sprintf("%.2f B/s", bps)
			} else if bps < 1024*1024 {
				return fmt.Sprintf("%.2f kb/s", bps/1024*8)
			}
			return fmt.Sprintf("%.2f Mb/s", bps/(1024*1024)*8)
		},
		"formatUptime": func(v interface{}) string {
			sec := toInt64(v)
			if sec <= 0 {
				return "0m"
			}
			days := sec / 86400
			hours := (sec % 86400) / 3600
			mins := (sec % 3600) / 60
			if days > 0 {
				return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
			}
			if hours > 0 {
				return fmt.Sprintf("%dh %dm", hours, mins)
			}
			return fmt.Sprintf("%dm", mins)
		},
		"formatPercent": func(v interface{}) string {
			pct := toFloat64(v)
			return fmt.Sprintf("%.1f", pct)
		},
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"join":      strings.Join,
		"hasPrefix": strings.HasPrefix,
		"contains":  strings.Contains,
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"add": func(a, b interface{}) float64 {
			return toFloat64(a) + toFloat64(b)
		},
		"sub": func(a, b interface{}) float64 {
			return toFloat64(a) - toFloat64(b)
		},
		"mul": func(a, b interface{}) float64 {
			return toFloat64(a) * toFloat64(b)
		},
		"div": func(a, b interface{}) float64 {
			denom := toFloat64(b)
			if denom == 0 {
				return 0
			}
			return toFloat64(a) / denom
		},
		"gt": func(a, b interface{}) bool {
			return toFloat64(a) > toFloat64(b)
		},
		"lt": func(a, b interface{}) bool {
			return toFloat64(a) < toFloat64(b)
		},
		"gte": func(a, b interface{}) bool {
			return toFloat64(a) >= toFloat64(b)
		},
		"lte": func(a, b interface{}) bool {
			return toFloat64(a) <= toFloat64(b)
		},
		"clampPercent": func(v interface{}) string {
			f := toFloat64(v)
			if f < 1.0 {
				return "1"
			}
			if f > 100.0 {
				return "100"
			}
			return fmt.Sprintf("%.1f", f)
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid dict call")
			}
			dict := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
	}
}

// Engine compiles and renders embedded Go templates
type Engine struct {
	tmpl *template.Template
}

// NewEngine creates a new template engine from embedded templates
func NewEngine() (*Engine, error) {
	root := template.New("").Funcs(funcMap())
	err := fs.WalkDir(templateFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := templateFiles.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path, "templates/")
		name = strings.ReplaceAll(name, "\\", "/")
		t := root.New(name)
		_, err = t.Parse(string(data))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}
	return &Engine{tmpl: root}, nil
}

// Render executes a named template to the provided writer
func (e *Engine) Render(w io.Writer, name string, data interface{}) error {
	name = strings.ReplaceAll(name, "\\", "/")
	t := e.tmpl.Lookup(name)
	if t == nil {
		return fmt.Errorf("template not found: %s", name)
	}
	return t.Execute(w, data)
}
