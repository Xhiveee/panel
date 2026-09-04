// Package web embeds and serves the master UI (templates + static assets).
package web

import (
	"embed"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// pages is the list of rendered pages; each file must define "body".
var pages = []string{
	"login", "dashboard", "nodes", "instances", "instance", "users", "settings",
}

// pagesMeta is the document title per page.
var pagesMeta = map[string]string{
	"login": "Вход", "dashboard": "Панель", "nodes": "Ноды",
	"instances": "Инстансы", "instance": "Инстанс",
	"users": "Пользователи", "settings": "Настройки",
}

// Renderer parses one template set per page (layout + navbar + page).
type Renderer struct {
	sets      map[string]*template.Template
	static    http.Handler
	vendorCSS template.CSS
}

// pageData is passed to every template.
type pageData struct {
	Title     string
	Page      string
	VendorCSS template.CSS
}

// NewRenderer parses templates and prepares the static handler.
func NewRenderer() (*Renderer, error) {
	vendorCSS := ""
	for _, f := range []string{"static/css/vendor/xterm.min.css", "static/css/vendor/uplot.min.css"} {
		b, err := staticFS.ReadFile(f)
		if err != nil {
			return nil, err
		}
		vendorCSS += string(b) + "\n"
	}

	sets := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		t, err := template.ParseFS(templatesFS,
			"templates/layout.html", "templates/nav.html", "templates/"+p+".html")
		if err != nil {
			return nil, err
		}
		sets[p] = t
	}

	return &Renderer{
		sets:      sets,
		static:    http.FileServer(http.FS(staticFS)),
		vendorCSS: template.CSS(vendorCSS),
	}, nil
}

// Render writes a page. It never returns template errors to the client.
func (r *Renderer) Render(w http.ResponseWriter, page, title string) {
	t, ok := r.sets[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	if title == "" {
		title = pagesMeta[page]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := pageData{Title: title, Page: page, VendorCSS: r.vendorCSS}
	_ = t.ExecuteTemplate(w, "layout.html", data)
}

// Handler returns the full UI http.Handler: /static/* plus page routes.
func Handler() (http.Handler, error) {
	r, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", r.static)

	for p, title := range pagesMeta {
		if p == "instance" {
			continue // /instances/{id} is registered below
		}
		page, t := p, title
		mux.HandleFunc("/"+page, func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/"+page {
				http.NotFound(w, req)
				return
			}
			r.Render(w, page, t)
		})
	}
	// The instance detail page lives at /instances/{id}.
	mux.HandleFunc("/instances/", func(w http.ResponseWriter, req *http.Request) {
		rest := strings.TrimPrefix(req.URL.Path, "/instances/")
		if rest == "" || strings.Contains(rest, "/") {
			http.NotFound(w, req)
			return
		}
		if _, err := strconv.ParseInt(rest, 10, 64); err != nil {
			http.NotFound(w, req)
			return
		}
		r.Render(w, "instance", pagesMeta["instance"])
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		http.Redirect(w, req, "/dashboard", http.StatusFound)
	})
	return mux, nil
}
