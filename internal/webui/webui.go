package webui

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sync"

	"github.com/walterschell/rip-bastion/internal/sysinfo"
)

const tmplSource = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta http-equiv="refresh" content="5">
    <title>rip-bastion status</title>
    <style>
        body { background: #1a1a2e; color: #e0e0e0; font-family: monospace; padding: 20px; }
        h1 { color: #00d4ff; }
        .section { background: #16213e; border: 1px solid #0f3460; border-radius: 4px; padding: 15px; margin: 10px 0; }
        .label { color: #888; }
        .value { color: #fff; font-weight: bold; }
        .ok { color: #00ff88; }
        .err { color: #ff0000; }
        .messages { background: #0d0d1a; padding: 10px; border-radius: 4px; }
        .msg { color: #aaffaa; margin: 2px 0; }
    </style>
</head>
<body>
    <h1>&#x1F510; rip-bastion</h1>
    {{ if .Network }}
    <div class="section">
        <h3>Network</h3>
        <p><span class="label">Interface: </span><span class="value">{{.Network.InterfaceName}}</span></p>
        <p><span class="label">IP Address: </span><span class="value">{{.Network.CIDR}}</span></p>
        <p><span class="label">Gateway: </span><span class="value">{{.Network.Gateway}}</span></p>
        <p><span class="label">DNS: </span><span class="value">{{range .Network.DNS}}{{.}} {{end}}</span></p>
    </div>
    {{ end }}
    {{ if .MDNS }}
    <div class="section">
        <h3>mDNS</h3>
        <p><span class="label">Status: </span>
           <span class="{{ if .MDNS.Running }}ok{{ else }}err{{ end }}">
               {{ if .MDNS.Running }}&#x25CF; Running{{ else }}&#x25CB; Stopped{{ end }}
           </span>
        </p>
        <p><span class="label">Hostname: </span><span class="value">{{.MDNS.Hostname}}</span></p>
    </div>
    {{ end }}
    {{ if .VPN }}
    <div class="section">
        <h3>VPN ({{.VPN.Name}})</h3>
        <p><span class="label">Status: </span>
           <span class="{{ if .VPN.Connected }}ok{{ else }}err{{ end }}">
               {{ if .VPN.Connected }}&#x25CF; Connected{{ else }}&#x25CB; Disconnected{{ end }}
           </span>
        </p>
        {{ if .VPN.Connected }}
        <p><span class="label">Interface: </span><span class="value">{{.VPN.Interface}}</span></p>
        <p><span class="label">Peer / Node: </span><span class="value">{{.VPN.PeerIP}}</span></p>
        {{ end }}
    </div>
    {{ end }}
    {{ if .Messages }}
    <div class="section">
        <h3>Messages</h3>
        <div class="messages">
            {{range .Messages}}<p class="msg">&#x25B8; {{.}}</p>{{end}}
        </div>
    </div>
    {{ end }}
</body>
</html>`

// Server serves a web UI showing the current system snapshot.
type Server struct {
	mu   sync.RWMutex
	snap *sysinfo.Snapshot
	tmpl *template.Template
	addr string
}

// New creates a new web UI server listening on addr (e.g. ":8080").
func New(addr string) (*Server, error) {
	tmpl, err := template.New("status").Parse(tmplSource)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	return &Server{
		tmpl: tmpl,
		addr: addr,
	}, nil
}

// Update updates the snapshot shown by the web UI (thread-safe).
func (s *Server) Update(snap *sysinfo.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

// Start starts the HTTP server in a background goroutine.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	go func() {
		log.Printf("Web UI listening on %s", s.addr)
		if err := http.ListenAndServe(s.addr, mux); err != nil {
			log.Printf("Web UI server error: %v", err)
		}
	}()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snap := s.snap
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if snap == nil {
		snap = &sysinfo.Snapshot{}
	}
	if err := s.tmpl.Execute(w, snap); err != nil {
		log.Printf("template execute error: %v", err)
	}
}
