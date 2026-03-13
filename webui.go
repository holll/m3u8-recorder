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
	w.mux.HandleFunc("/delete", w.handleDelete)
	w.mux.HandleFunc("/status", w.handleStatus)
}

var indexTpl = template.Must(template.New("index").Parse(`
<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>M3U8 Recorder</title>
  <style>
    :root {
      --bg: #f3f6fb;
      --panel: #ffffff;
      --border: #dce5f1;
      --text: #1f2a44;
      --subtle: #6c7a96;
      --primary: #4f6ef7;
      --danger: #ea5b66;
      --success-bg: #dff7ef;
      --success-text: #00a16a;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      padding: 16px;
      background: var(--bg);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
    }
    .container {
      max-width: 1200px;
      margin: 0 auto;
      display: grid;
      gap: 14px;
    }
    .panel {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 14px;
      padding: 16px;
      box-shadow: 0 6px 22px rgba(28, 50, 88, 0.06);
    }
    h2 { margin: 0 0 12px; }
    h3 { margin: 0 0 12px; color: #3d5685; }
    .controls {
      display: flex;
      gap: 10px;
      margin-bottom: 10px;
    }
    input {
      flex: 1;
      min-width: 240px;
      border: 1px solid #c8d4e8;
      border-radius: 10px;
      padding: 10px 12px;
      font-size: 14px;
      outline: none;
      transition: border-color .2s;
    }
    input:focus { border-color: var(--primary); }
    button {
      border: 0;
      border-radius: 10px;
      padding: 10px 14px;
      font-size: 14px;
      font-weight: 600;
      color: #fff;
      background: var(--primary);
      cursor: pointer;
    }
    button:hover { opacity: .92; }
    button:disabled {
      cursor: not-allowed;
      background: #c4cbda;
      color: #f8f9fc;
    }
    .btn-danger { background: var(--danger); }
    .status-line {
      margin: 6px 0 12px;
      color: var(--subtle);
      font-size: 14px;
    }
    .stats {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
    }
    .pill {
      border-radius: 999px;
      background: #ecf2ff;
      color: #3858a6;
      padding: 6px 10px;
      font-size: 13px;
      font-weight: 600;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      table-layout: fixed;
    }
    th, td {
      border-bottom: 1px solid #e4ebf7;
      padding: 10px 8px;
      text-align: left;
      vertical-align: top;
      font-size: 14px;
      word-break: break-all;
    }
    th { color: #4a638f; font-weight: 700; }
    .state-badge {
      display: inline-block;
      padding: 4px 10px;
      border-radius: 999px;
      font-size: 12px;
      font-weight: 700;
      background: #eef2fb;
      color: #3e5685;
    }
    .state-running { background: var(--success-bg); color: var(--success-text); }
    .actions button { margin-right: 6px; }
    .actions button:last-child { margin-right: 0; }
    .room-name { font-weight: 700; color: #2e4676; }
  </style>
</head>
<body>
<div class="container">
  <section class="panel">
    <h2>M3U8 Recorder（多路并发）</h2>
    <div class="controls">
      <input id="url" placeholder="paste m3u8 url here" />
      <button onclick="addRoom()">添加房间</button>
      <button class="btn-danger" onclick="stopAll()">全部停止</button>
    </div>
    <p id="schedule" class="status-line"></p>
    <div class="stats">
      <span class="pill" id="roomCount">房间数：0</span>
      <span class="pill" id="activeCount">录制中：0</span>
      <span class="pill" id="totalTraffic">累计流量：0 B</span>
    </div>
  </section>

  <section class="panel">
    <h3>房间状态（录制时长 / 录制速度）</h3>
    <table>
      <thead>
        <tr>
          <th style="width:22%">房间名称</th>
          <th style="width:29%">URL</th>
          <th style="width:8%">状态</th>
          <th style="width:9%">录制时长</th>
          <th style="width:10%">录制速度</th>
          <th style="width:7%">分片数</th>
          <th style="width:10%">累计流量</th>
          <th style="width:8%">错误</th>
          <th style="width:7%">操作</th>
        </tr>
      </thead>
      <tbody id="rooms"></tbody>
    </table>
  </section>
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

function updateSummary(data) {
  const rooms = data.rooms || [];
  let totalBytes = 0;
  rooms.forEach(room => {
    totalBytes += Number(room.bytes_done || 0);
  });
  document.getElementById('roomCount').textContent = '房间数：' + rooms.length;
  document.getElementById('activeCount').textContent = '录制中：' + Number(data.active_count || 0);
  document.getElementById('totalTraffic').textContent = '累计流量：' + formatBytes(totalBytes);
}

function renderRooms(rooms) {
  const tbody = document.getElementById('rooms');
  tbody.innerHTML = '';
  if (!rooms || rooms.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9">暂无房间</td></tr>';
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
    const encodedURL = encodeURIComponent(room.url || '');
    let actionBtn = '';
    if (canStop) {
      actionBtn = '<button class="btn-danger" onclick="stopOneByUrl(this.dataset.url)" data-url="' + encodedURL + '">停止</button>';
    } else if (canStart) {
      actionBtn = '<button onclick="startByUrl(this.dataset.url)" data-url="' + encodedURL + '">启动</button>';
    } else {
      actionBtn = '<button disabled>处理中</button>';
    }
    actionBtn += '<button class="btn-danger" onclick="deleteRoomByUrl(this.dataset.url)" data-url="' + encodedURL + '">删除</button>';
    const stateClass = room.state === 'running' ? 'state-badge state-running' : 'state-badge';
    tr.innerHTML =
      '<td class="room-name">' + (room.room_base_name || '') + '</td>' +
      '<td>' + (room.url || '') + '</td>' +
      '<td><span class="' + stateClass + '">' + stateText + '</span></td>' +
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
  updateSummary(data);
  renderRooms(data.rooms || []);
}

async function addRoom() {
  const input = document.getElementById('url');
  const url = input.value.trim();
  const r = await fetch('/add', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
  const data = await r.json();
  alert(data.message);
  if (r.ok) {
    input.value = '';
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

async function deleteRoomByUrl(encodedURL) {
  const url = decodeURIComponent(encodedURL || '');
  if (!confirm('确认删除该直播间？')) {
    return;
  }
  const r = await fetch('/delete', {
    method:'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({url})
  });
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

func (w *WebUI) handleDelete(rw http.ResponseWriter, r *http.Request) {
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
	if err := w.rec.DeleteRoom(req.URL); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"message": "room deleted"})
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}
