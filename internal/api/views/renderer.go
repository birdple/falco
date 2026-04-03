package views

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"path/filepath"
)

// Templates is an embedded filesystem containing all templates
//
//go:embed *.html partials/*.html
var templatesFS embed.FS

type Renderer struct {
	templates map[string]*template.Template
}

func NewRenderer() (*Renderer, error) {
	r := &Renderer{
		templates: make(map[string]*template.Template),
	}

	// Parse layout and pages
	pages, err := templatesFS.ReadDir(".")
	if err != nil {
		return nil, err
	}

	for _, page := range pages {
		if page.IsDir() || filepath.Ext(page.Name()) != ".html" || page.Name() == "layout.html" {
			continue
		}

		// Each page is parsed with the layout and all partials
		tmpl := template.New(page.Name()).Funcs(template.FuncMap{
			"humanizeBytes": HumanizeBytes,
		})
		tmpl, err = tmpl.ParseFS(templatesFS, "layout.html", page.Name(), "partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("error parsing template %s: %w", page.Name(), err)
		}
		r.templates[page.Name()] = tmpl
	}

	return r, nil
}

func (r *Renderer) Render(w io.Writer, name string, data interface{}) error {
	tmpl, ok := r.templates[name]
	if !ok {
		return fmt.Errorf("template %s not found", name)
	}
	return tmpl.ExecuteTemplate(w, "layout", data)
}

func (r *Renderer) RenderPartial(w io.Writer, name string, data interface{}) error {
	tmpl, ok := r.templates[name]
	if !ok {
		// Fallback to searching in all templates if it's a specific partial name not associated with a page
		// For simplicity, we might want to pre-parse partials too or just execute by name
		return fmt.Errorf("partial/template %s not found", name)
	}
	// "content" is usually the block name in partials
	return tmpl.ExecuteTemplate(w, name, data)
}
