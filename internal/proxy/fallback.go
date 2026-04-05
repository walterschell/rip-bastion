package proxy

import (
	"html/template"
	"net/http"
)

const fallbackTmplSrc = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            background: #0f172a;
            color: #e2e8f0;
            font-family: 'Segoe UI', system-ui, sans-serif;
            min-height: 100vh;
            display: flex;
            flex-direction: column;
            align-items: center;
            padding: 48px 16px;
        }
        h1 {
            font-size: 2rem;
            color: #38bdf8;
            margin-bottom: 8px;
            letter-spacing: -0.5px;
        }
        .subtitle {
            color: #64748b;
            margin-bottom: 40px;
            font-size: 0.9rem;
        }
        .routes {
            width: 100%;
            max-width: 640px;
            display: flex;
            flex-direction: column;
            gap: 12px;
        }
        .route-card {
            background: #1e293b;
            border: 1px solid #334155;
            border-radius: 8px;
            padding: 16px 20px;
            text-decoration: none;
            color: inherit;
            display: flex;
            justify-content: space-between;
            align-items: center;
            transition: border-color 0.15s, background 0.15s;
        }
        .route-card:hover {
            border-color: #38bdf8;
            background: #1e3a5f;
        }
        .route-name {
            font-weight: 600;
            font-size: 1rem;
            color: #f1f5f9;
        }
        .route-host {
            font-size: 0.85rem;
            color: #38bdf8;
            font-family: monospace;
        }
        .route-target {
            font-size: 0.8rem;
            color: #64748b;
            font-family: monospace;
        }
        .route-info { display: flex; flex-direction: column; gap: 2px; }
        .arrow { color: #475569; font-size: 1.2rem; }
        .empty {
            color: #475569;
            font-style: italic;
            margin-top: 32px;
        }
        footer {
            margin-top: 48px;
            color: #334155;
            font-size: 0.75rem;
        }
    </style>
</head>
<body>
    <h1>&#x1F510; {{.Title}}</h1>
    <p class="subtitle">Select a destination</p>

    <div class="routes">
    {{range .Routes}}
        <a class="route-card" href="https://{{.Host}}">
            <div class="route-info">
                <span class="route-name">{{.Name}}</span>
                <span class="route-host">{{.Host}}</span>
                <span class="route-target">&#x2192; {{.Target}}</span>
            </div>
            <span class="arrow">&#x276F;</span>
        </a>
    {{else}}
        <p class="empty">No routes configured. Add routes to your proxy.yaml.</p>
    {{end}}
    </div>

    <footer>rip-bastion built-in proxy</footer>
</body>
</html>`

var fallbackTmpl = template.Must(template.New("fallback").Parse(fallbackTmplSrc))

type fallbackData struct {
	Title  string
	Routes []Route
}

// serveFallback writes the route-listing page as an HTML response.
func serveFallback(w http.ResponseWriter, r *http.Request, title string, routes []Route) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = fallbackTmpl.Execute(w, fallbackData{
		Title:  title,
		Routes: routes,
	})
}
