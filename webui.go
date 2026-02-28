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
    :root {
      --bg: #f4f7fb;
      --card: #ffffff;
      --line: #e5ebf3;
      --text: #243046;
      --sub: #6a7890;
      --primary: #3d7bff;
      --danger: #e85a5a;
      --success: #1aa772;
      --idle: #8a93a5;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background: linear-gradient(180deg, #f8fbff 0%, var(--bg) 100%);
      color: var(--text);
    }
    .container { max-width: 1180px; margin: 24px auto; padding: 0 16px; }
    .card {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 14px;
      box-shadow: 0 8px 26px rgba(36, 48, 70, 0.06);
      padding: 16px;
      margin-bottom: 14px;
    }
    h2, h3 { margin: 0; }
    h2 { font-size: 22px; }
    h3 { font-size: 16px; color: var(--sub); font-weight: 600; }
    .toolbar { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 12px; }
    input {
      flex: 1 1 580px;
      min-width: 220px;
      padding: 10px 12px;
      border: 1px solid #d5deea;
      border-radius: 10px;
      outline: none;
      font-size: 14px;
    }
    input:focus { border-color: var(--primary); box-shadow: 0 0 0 3px rgba(61,123,255,.15); }
    button {
      border: 0;
      border-radius: 10px;
      padding: 10px 12px;
      font-size: 13px;
      font-weight: 600;
      color: white;
      background: var(--primary);
      cursor: pointer;
    }
    button:hover { filter: brightness(0.95); }
    button:disabled { cursor: not-allowed; background: #aeb8c8; }
    .btn-danger { background: var(--danger); }
    .btn-ghost { background: #7182a0; }
    .meta { color: var(--sub); margin-top: 8px; font-size: 13px; }
    .chips { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 12px; }
    .chip {
      background: #edf3ff;
      color: #345089;
      border-radius: 999px;
      padding: 6px 10px;
      font-size: 12px;
      font-weight: 600;
    }
    table { width:100%; border-collapse: collapse; margin-top: 12px; }
    th, td { border-bottom:1px solid var(--line); padding: 10px 8px; text-align: left; font-size: 13px; vertical-align: top; }
    th { color: var(--sub); font-weight: 600; }
    tr:hover td { background: #fafcff; }
    .url-cell { max-width: 360px; word-break: break-all; }
    .actions { display: flex; gap: 6px; flex-wrap: wrap; }
    .status {
      display: inline-block;
      font-size: 12px;
      font-weight: 600;
      border-radius: 999px;
      padding: 3px 8px;
      background: #eef1f6;
      color: var(--idle);
    }
    .status-running { background: rgba(26,167,114,.12); color: var(--success); }
    .status-stopping { background: rgba(232,90,90,.12); color: #cc4a4a; }
    .status-error { background: rgba(232,90,90,.12); color: #c83d3d; }
  </style>
</head>
<body>
  <div class="container">
  <div class="card">
    <h2>M3U8 Recorder（多路并发）</h2>
    <div class="toolbar">
      <input id="url" placeholder="粘贴 m3u8 直播流地址" />
      <button onclick="start()">添加录制</button>
      <button class="btn-danger" onclick="stopAll()">全部停止</button>
    </div>
    <p id="schedule" class="meta"></p>
    <div class="chips">
      <span class="chip" id="statRooms">房间数：0</span>
      <span class="chip" id="statActive">录制中：0</span>
      <span class="chip" id="statTotalBytes">累计流量：0 B</span>
    </div>
  </div>

  <div class="card">
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
  </div>
  </div>

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
    const stateClass = room.state ? ('status-' + room.state) : '';
    const canStart = !!room.can_start;
    const actionBtn = canStop
      ? '<button class="btn-danger" onclick="stopOneByUrl(this.dataset.url)" data-url="' + encodeURIComponent(room.url || '') + '">停止</button>'
      : (canStart
          ? '<button onclick="startByUrl(this.dataset.url)" data-url="' + encodeURIComponent(room.url || '') + '">启动</button>'
          : '<button class="btn-ghost" disabled>处理中</button>');
    tr.innerHTML =
      '<td class="url-cell">' + (room.url || '') + '</td>' +
      '<td><span class="status ' + stateClass + '">' + stateText + '</span></td>' +
      '<td>' + formatDuration(room.uptime_seconds) + '</td>' +
      '<td>' + (room.speed_kb_per_s || 0).toFixed(2) + ' KB/s</td>' +
      '<td>' + (room.segments_done || 0) + '</td>' +
      '<td>' + formatBytes(room.bytes_done) + '</td>' +
      '<td>' + (room.error || '') + '</td>' +
      '<td class="actions">' + actionBtn + '</td>';
    tbody.appendChild(tr);
  });
}

function renderSummary(data) {
  const rooms = data.rooms || [];
  let total = 0;
  rooms.forEach(room => { total += Number(room.bytes_done || 0); });
  document.getElementById('statRooms').textContent = '房间数：' + rooms.length;
  document.getElementById('statActive').textContent = '录制中：' + Number(data.active_count || 0);
  document.getElementById('statTotalBytes').textContent = '累计流量：' + formatBytes(total);
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
  renderSummary(data);
  renderRooms(data.rooms || []);
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
