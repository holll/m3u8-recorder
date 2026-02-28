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
	w.mux.HandleFunc("/add", w.handleAdd)
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
    body { font-family: sans-serif; max-width: 1100px; margin: 30px auto; }
    input { width: 75%; padding: 8px; }
    button { padding: 8px 12px; margin-left: 6px; }
    pre { background:#f6f6f6; padding:12px; overflow:auto; }
    table { width:100%; border-collapse: collapse; margin-top: 14px; }
    th, td { border:1px solid #ddd; padding: 8px; text-align: left; font-size: 14px; }
    th { background: #f7f7f7; }
    .actions button { margin-left: 0; }
  </style>
</head>
<body>
  <h2>M3U8 Recorder（多路并发）</h2>
  <div>
    <input id="url" placeholder="paste m3u8 url here" />
    <button onclick="addRoom()">添加房间</button>
    <button onclick="stopAll()">全部停止</button>
  </div>
  <p id="schedule" style="color:#666; margin-top:8px;"></p>

  <h3>房间状态（录制时长 / 录制速度）</h3>
  <table>
    <thead>
      <tr>
        <th>URL</th>
        <th>状态</th>
        <th>录制时长</th>
        <th>录制速度</th>
        <th>分片数</th>
        <th>累计流量</th>
        <th>错误</th>
        <th>操作</th>
      </tr>
    </thead>
    <tbody id="rooms"></tbody>
  </table>

  <h3>原始状态 JSON</h3>
  <pre id="status">loading...</pre>

<script>
function formatDuration(sec) {
  sec = Number(sec || 0);
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  return [h, m, s].map(v => String(v).padStart(2, '0')).join(':');
}

function formatBytes(n) {
  n = Number(n || 0);
  if (n < 1024) return n.toFixed(0) + ' B';
  if (n < 1024*1024) return (n/1024).toFixed(2) + ' KB';
  if (n < 1024*1024*1024) return (n/1024/1024).toFixed(2) + ' MB';
  return (n/1024/1024/1024).toFixed(2) + ' GB';
}

function renderRooms(rooms) {
  const tbody = document.getElementById('rooms');
  tbody.innerHTML = '';
  if (!rooms || rooms.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8">暂无房间</td></tr>';
    return;
  }

  rooms.forEach(room => {
    const tr = document.createElement('tr');
    const canStop = !!room.can_stop;
    const stateTextMap = {
      running: '录制中',
      stopping: '停止中',
      idle: '已停止',
      error: '错误'
    };
    const stateText = stateTextMap[room.state] || (room.state || '');
    const canStart = !!room.can_start;
    const actionBtn = canStop
      ? '<button onclick="stopOneByUrl(this.dataset.url)" data-url="' + encodeURIComponent(room.url || '') + '">停止</button>'
      : (canStart
          ? '<button onclick="startByUrl(this.dataset.url)" data-url="' + encodeURIComponent(room.url || '') + '">启动</button>'
          : '<button disabled>处理中</button>');
    tr.innerHTML =
      '<td>' + (room.url || '') + '</td>' +
      '<td>' + stateText + '</td>' +
      '<td>' + formatDuration(room.uptime_seconds) + '</td>' +
      '<td>' + (room.speed_kb_per_s || 0).toFixed(2) + ' KB/s</td>' +
      '<td>' + (room.segments_done || 0) + '</td>' +
      '<td>' + formatBytes(room.bytes_done) + '</td>' +
      '<td>' + (room.error || '') + '</td>' +
      '<td class="actions">' + actionBtn + '</td>';
    tbody.appendChild(tr);
  });
}

function renderSchedule(data) {
  const el = document.getElementById('schedule');
  if (!data.schedule_enabled) {
    el.textContent = '定时录制：未启用（全天可录制）';
    return;
  }
  const phase = data.schedule_in_range ? '当前在录制时段' : '当前不在录制时段';
  el.textContent = '定时录制：' + data.schedule_start + ' ~ ' + data.schedule_end + '（' + phase + '）';
}

async function refresh() {
  const r = await fetch('/status');
  const data = await r.json();
  renderSchedule(data);
  renderRooms(data.rooms || []);
  document.getElementById('status').textContent = JSON.stringify(data, null, 2);
}

async function addRoom() {
  const url = document.getElementById('url').value.trim();
  const r = await fetch('/add', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
  const result = await r.json();
  alert(result.message);
  if (r.ok) {
    document.getElementById('url').value = '';
  }
  refresh();
}

async function stopOneByUrl(encodedURL) {
  const url = decodeURIComponent(encodedURL || '');
  const r = await fetch('/stop', {
    method:'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
  alert((await r.json()).message);
  refresh();
}

async function startByUrl(encodedURL) {
  const url = decodeURIComponent(encodedURL || '');
  const r = await fetch('/start', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
  alert((await r.json()).message);
  refresh();
}

async function stopAll() {
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

func (w *WebUI) handleAdd(rw http.ResponseWriter, r *http.Request) {
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
	if err := w.rec.AddRoom(req.URL); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"message": "room added"})
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

	var req struct {
		URL string `json:"url"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if err := w.rec.Stop(req.URL); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if req.URL == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"message": "stopping all rooms"})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"message": "stopping"})
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}
