package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
)

type WebUI struct {
	rec *Recorder
	mux *http.ServeMux
}

func NewWebUI(rec *Recorder) *WebUI {
	w := &WebUI{
		rec: rec,
		mux: http.NewServeMux(),
	}
	w.routes()
	return w
}

func (w *WebUI) Listen(addr string) error {
	log.Printf("WebUI listening on %s", addr)
	return http.ListenAndServe(addr, w.mux)
}

func (w *WebUI) routes() {
	w.mux.HandleFunc("/", w.handleIndex)
	w.mux.HandleFunc("/start", w.handleStart)
	w.mux.HandleFunc("/stop", w.handleStop)
	w.mux.HandleFunc("/status", w.handleStatus)
}

var indexTpl = template.Must(template.New("index").Parse(`
<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <title>M3U8 Recorder</title>
  <style>
    body { font-family: sans-serif; max-width: 900px; margin: 30px auto; }
    input { width: 80%; padding: 8px; }
    button { padding: 8px 12px; margin-left: 6px; }
    pre { background:#f6f6f6; padding:12px; overflow:auto; }
  </style>
</head>
<body>
  <h2>M3U8 Recorder</h2>
  <div>
    <input id="url" placeholder="paste m3u8 url here" />
    <button onclick="start()">Start</button>
    <button onclick="stop()">Stop</button>
  </div>
  <h3>Status</h3>
  <pre id="status">loading...</pre>

<script>
async function refresh() {
  const r = await fetch('/status');
  document.getElementById('status').textContent = JSON.stringify(await r.json(), null, 2);
}
async function start() {
  const url = document.getElementById('url').value.trim();
  const r = await fetch('/start', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
  alert((await r.json()).message);
  refresh();
}
async function stop() {
  const r = await fetch('/stop', {method:'POST'});
  alert((await r.json()).message);
  refresh();
}
setInterval(refresh, 1000);
refresh();
</script>
</body>
</html>
`))

func (w *WebUI) handleIndex(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTpl.Execute(rw, nil)
}

func (w *WebUI) handleStatus(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, w.rec.GetStatus())
}

func (w *WebUI) handleStart(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": "bad json"})
		return
	}
	if err := w.rec.Start(req.URL); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"message": "started"})
}

func (w *WebUI) handleStop(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	if err := w.rec.Stop(); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"message": "stopping"})
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}
