package httpserver

import "net/http"

func (s *Server) handleAdminUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(adminHTML))
}

const adminHTML = `<!DOCTYPE html>
<html lang="zh-Hans">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Kiln</title>
<style>
:root { color-scheme: light dark; --bg:#0f1115; --card:#171a21; --fg:#e8eaed; --muted:#9aa0a6; --acc:#7cb3ff; --ok:#3dd68c; --bad:#ff7b72; --line:#2a2f3a; }
@media (prefers-color-scheme: light) {
  :root { --bg:#f4f6f8; --card:#fff; --fg:#1a1d23; --muted:#5f6368; --acc:#1a73e8; --ok:#137333; --bad:#c5221f; --line:#e0e3e8; }
}
* { box-sizing: border-box; }
body { margin:0; font:14px/1.45 system-ui,sans-serif; background:var(--bg); color:var(--fg); }
header { padding:16px 20px; border-bottom:1px solid var(--line); display:flex; gap:12px; align-items:center; flex-wrap:wrap; }
header h1 { font-size:18px; margin:0; font-weight:600; }
main { max-width:1120px; margin:0 auto; padding:16px 20px 48px; display:grid; gap:16px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:16px; }
.card h2 { margin:0 0 12px; font-size:15px; }
.row { display:flex; gap:8px; flex-wrap:wrap; align-items:center; margin-bottom:8px; }
label { color:var(--muted); font-size:12px; }
input, select, textarea, button { font:inherit; }
input, select, textarea { background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:8px; padding:8px 10px; min-width:0; }
textarea { width:100%; min-height:120px; font-family:ui-monospace,Menlo,monospace; font-size:12px; }
button { border:0; border-radius:8px; padding:8px 12px; background:var(--acc); color:#fff; cursor:pointer; }
button.secondary { background:transparent; color:var(--fg); border:1px solid var(--line); }
button.danger { background:var(--bad); }
button:disabled { opacity:.5; cursor:default; }
table { width:100%; border-collapse:collapse; font-size:13px; }
th, td { text-align:left; padding:8px 6px; border-bottom:1px solid var(--line); vertical-align:top; }
th { color:var(--muted); font-weight:500; }
.muted { color:var(--muted); }
.ok { color:var(--ok); }
.bad { color:var(--bad); }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size:12px; word-break:break-all; }
.hidden { display:none !important; }
#toast { position:fixed; right:16px; bottom:16px; background:var(--card); border:1px solid var(--line); padding:10px 14px; border-radius:10px; max-width:440px; display:none; z-index:9; }
.ord { display:inline-flex; gap:4px; }
.ord button { padding:4px 8px; font-size:12px; }
</style>
</head>
<body>
<header>
  <h1>Kiln</h1>
  <span class="muted" id="ver"></span>
  <div style="flex:1"></div>
  <span id="who" class="muted"></span>
  <button class="secondary" id="btnLogout" type="button">退出登录</button>
</header>
<main>
  <section class="card" id="loginCard">
    <h2>登录</h2>
    <div class="row">
      <input id="user" placeholder="用户名" value="admin" autocomplete="username"/>
      <input id="pass" placeholder="密码" type="password" autocomplete="current-password"/>
      <button id="btnLogin" type="button">登录</button>
    </div>
    <p class="muted">用于配置频道、访问链接与出站规则。请使用外部播放器访问播放地址。</p>
  </section>

  <section class="card hidden" id="app">
    <h2>状态</h2>
    <div class="row muted" id="statusLine">—</div>
    <div class="row">
      <button class="secondary" id="btnRefresh" type="button">更新</button>
    </div>
  </section>

  <section class="card hidden" id="chCard">
    <h2>频道</h2>
    <div class="row">
      <input id="chId" placeholder="标识符" style="width:120px"/>
      <input id="chTitle" placeholder="名称" style="width:140px"/>
      <input id="chGroup" placeholder="分组" style="width:90px"/>
      <select id="chUp"></select>
      <input id="chPath" placeholder="路径" style="flex:1;min-width:140px"/>
      <select id="chIngress"><option value="dash">DASH</option><option value="hls">HLS</option></select>
      <input id="chKeys" placeholder="密钥文件" style="width:160px"/>
      <input id="chPref" placeholder="高度" type="number" style="width:90px"/>
      <button id="btnSaveCh" type="button">存储</button>
    </div>
    <table>
      <thead><tr><th>顺序</th><th>标识符</th><th>名称</th><th>上游 / 路径</th><th>类型</th><th></th></tr></thead>
      <tbody id="chBody"></tbody>
    </table>
  </section>

  <section class="card hidden" id="impCard">
    <h2>导入播放列表</h2>
    <p class="muted">粘贴 M3U 内容以预览映射结果，确认后写入数据库。同标识符将更新现有频道，不会删除其他频道。</p>
    <div class="row">
      <select id="impUp"></select>
      <input id="impKeys" placeholder="DASH 默认密钥文件" style="flex:1"/>
      <input id="impPref" type="number" placeholder="首选高度" style="width:110px"/>
    </div>
    <textarea id="impText" placeholder="#EXTM3U&#10;#EXTINF:..."></textarea>
    <div class="row" style="margin-top:8px">
      <button class="secondary" id="btnImpPreview" type="button">预览</button>
      <button id="btnImpApply" type="button" disabled>导入</button>
    </div>
    <div id="impPrev" class="muted" style="margin-top:8px"></div>
  </section>

  <section class="card hidden" id="tokCard">
    <h2>访问链接</h2>
    <p class="muted">令牌为 v1 前缀与 126 位 base62。系统仅存储哈希；明文令牌仅在创建时显示一次。</p>
    <div class="row">
      <input id="tokName" placeholder="名称" style="width:160px"/>
      <input id="tokScope" placeholder="频道标识符，逗号分隔；留空表示全部" style="flex:1"/>
      <input id="tokNote" placeholder="备注" style="width:120px"/>
      <button id="btnNewTok" type="button">创建</button>
    </div>
    <div id="tokOnce" class="hidden" style="margin:8px 0;padding:10px;border:1px dashed var(--line);border-radius:8px">
      <div class="muted">请立即拷贝以下内容</div>
      <div class="mono" id="tokPlain"></div>
      <div class="mono" id="tokURL"></div>
      <div class="row" style="margin-top:8px">
        <button class="secondary" id="btnCopyTok" type="button">拷贝令牌</button>
        <button class="secondary" id="btnCopyURL" type="button">拷贝播放列表地址</button>
      </div>
    </div>
    <table>
      <thead><tr><th>名称</th><th>前缀</th><th>范围</th><th>状态</th><th></th></tr></thead>
      <tbody id="tokBody"></tbody>
    </table>
  </section>

  <section class="card hidden" id="logCard">
    <h2>访问记录</h2>
    <p class="muted">记录播放列表与播放入口请求，不含媒体分片。最多保留约 5000 条。</p>
    <div class="row">
      <button class="secondary" id="btnLogs" type="button">更新记录</button>
    </div>
    <table>
      <thead><tr><th>时间</th><th>前缀</th><th>路径</th><th>频道</th><th>状态</th><th>来源</th></tr></thead>
      <tbody id="logBody"></tbody>
    </table>
  </section>

  <section class="card hidden" id="egCard">
    <h2>出站代理</h2>
    <p class="muted">播放器仅需连接 Kiln。规则与配置写入数据库后立即生效。经代理访问部分 CDN 时，系统将优先使用 HTTPS。</p>
    <div class="row">
      <label>默认代理</label>
      <input id="egDefault" style="width:120px" placeholder="direct"/>
      <label>播放列表策略</label>
      <select id="egPolicy">
        <option value="rewrite">rewrite</option>
        <option value="passthrough">passthrough</option>
        <option value="auto">auto</option>
      </select>
      <button id="btnEgSaveMeta" type="button">存储策略</button>
    </div>
    <div class="row">
      <input id="egPxId" placeholder="代理标识符" style="width:110px"/>
      <input id="egPxName" placeholder="名称" style="width:100px"/>
      <input id="egPxURL" placeholder="http://127.0.0.1:7890 或 socks5h://127.0.0.1:6153" style="flex:1"/>
      <button id="btnEgSavePx" type="button">存储代理</button>
    </div>
    <div id="egProxies" class="mono muted" style="margin-bottom:8px"></div>
    <div class="row">
      <input id="egRuleId" placeholder="规则标识符" style="width:100px"/>
      <input id="egRulePri" type="number" placeholder="优先级" style="width:80px" value="10"/>
      <select id="egRuleKind">
        <option value="host_suffix">host_suffix</option>
        <option value="host_exact">host_exact</option>
        <option value="host_regex">host_regex</option>
        <option value="channel_id">channel_id</option>
        <option value="url_regex">url_regex</option>
      </select>
      <input id="egRulePat" placeholder="匹配模式" style="flex:1"/>
      <input id="egRuleProxy" placeholder="代理标识符或 direct" style="width:120px"/>
      <button id="btnEgSaveRule" type="button">存储规则</button>
    </div>
    <table style="margin-top:8px">
      <thead><tr><th>规则</th><th>匹配</th><th>代理</th><th></th></tr></thead>
      <tbody id="egRules"></tbody>
    </table>
    <div class="row" style="margin-top:10px">
      <input id="egURL" placeholder="测试地址" style="flex:1"/>
      <input id="egCh" placeholder="频道标识符" style="width:120px"/>
      <button class="secondary" id="btnEgTest" type="button">测试</button>
    </div>
    <div id="egResult" class="mono muted"></div>
  </section>

  <section class="card hidden" id="setCard">
    <h2>设置</h2>
    <div class="row">
      <label>公共基址</label>
      <input id="setBase" style="flex:1" placeholder="http://kiln.lan:8080"/>
      <button id="btnSaveSet" type="button">存储</button>
    </div>
  </section>
</main>
<div id="toast"></div>
<script>
const state = { token: localStorage.getItem('kiln_admin_token') || '', channels: [], preview: null };
const $ = (id) => document.getElementById(id);
function toast(msg) {
  const el = $('toast'); el.textContent = msg; el.style.display = 'block';
  clearTimeout(window.__tt); window.__tt = setTimeout(() => el.style.display = 'none', 3200);
}
async function api(path, opt={}) {
  const headers = Object.assign({'Content-Type':'application/json'}, opt.headers||{});
  if (state.token) headers['Authorization'] = 'Bearer ' + state.token;
  const res = await fetch(path, Object.assign({}, opt, { headers, cache: 'no-store' }));
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { raw: text }; }
  if (!res.ok) throw new Error(data?.error?.message || res.statusText);
  return data;
}
function showApp(on) {
  $('loginCard').classList.toggle('hidden', on);
  ['app','chCard','impCard','tokCard','logCard','egCard','setCard'].forEach(id => $(id).classList.toggle('hidden', !on));
  $('btnLogout').classList.toggle('hidden', !on);
}
function esc(s){ return String(s??'').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function fmtTime(sec) {
  if (!sec) return '—';
  try { return new Date(sec*1000).toLocaleString(); } catch { return String(sec); }
}
async function login() {
  const data = await api('/v1/auth/login', { method:'POST', body: JSON.stringify({ username:$('user').value, password:$('pass').value }) });
  state.token = data.token; localStorage.setItem('kiln_admin_token', state.token);
  $('who').textContent = data.username + ' · ' + data.role;
  showApp(true); await refreshAll();
}
function logout() {
  state.token = ''; localStorage.removeItem('kiln_admin_token');
  $('who').textContent = ''; showApp(false);
}
async function reorder(ids) {
  await api('/v1/admin/channels/reorder', { method:'PUT', body: JSON.stringify({ ids }) });
  await refreshAll();
}
async function moveChannel(id, dir) {
  const ids = state.channels.map(c => c.id);
  const i = ids.indexOf(id);
  if (i < 0) return;
  const j = i + dir;
  if (j < 0 || j >= ids.length) return;
  [ids[i], ids[j]] = [ids[j], ids[i]];
  await reorder(ids);
}
async function refreshAll() {
  const [st, ch, ups, toks, set, eg] = await Promise.all([
    api('/v1/status'), api('/v1/admin/channels'), api('/v1/admin/upstreams'),
    api('/v1/admin/access-tokens'), api('/v1/admin/settings'), api('/v1/admin/egress')
  ]);
  const sess = (st.sessions||[]).map(s => s.channel_id + ':' + s.state).join(' · ') || '无活动会话';
  $('statusLine').innerHTML = '运行 ' + st.uptime_sec + ' 秒 · 请求 ' + st.requests + ' · 错误 ' + st.errors +
    ' · 会话 ' + st.session_count + ' <span class="muted">(' + sess + ')</span>';
  const fillUp = (sel) => {
    sel.innerHTML = '';
    (ups.upstreams||[]).forEach(u => {
      const o=document.createElement('option'); o.value=u.id; o.textContent=u.id+' ('+u.base_url+')'; sel.appendChild(o);
    });
  };
  fillUp($('chUp')); fillUp($('impUp'));
  state.channels = (ch.channels||[]).slice().sort((a,b)=>(a.sort_order||0)-(b.sort_order||0));
  const tb = $('chBody'); tb.innerHTML = '';
  state.channels.forEach((c, idx) => {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td class="ord"><button class="secondary" data-up="'+c.id+'">↑</button><button class="secondary" data-dn="'+c.id+'">↓</button> <span class="muted">'+(idx+1)+'</span></td>'+
      '<td class="mono">'+esc(c.id)+'</td><td>'+esc(c.title)+'</td><td class="mono">'+esc(c.upstream)+' '+esc(c.path||'')+
      '</td><td>'+esc(c.ingress)+(c.disabled?' <span class="bad">已停用</span>':'')+
      '</td><td><button class="secondary" data-probe="'+c.id+'">检测</button> <button class="danger" data-del="'+c.id+'">删除</button></td>';
    tb.appendChild(tr);
  });
  tb.querySelectorAll('[data-up]').forEach(b => b.onclick = () => moveChannel(b.dataset.up, -1).catch(e=>toast(e.message)));
  tb.querySelectorAll('[data-dn]').forEach(b => b.onclick = () => moveChannel(b.dataset.dn, 1).catch(e=>toast(e.message)));
  tb.querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
    if (!confirm('将删除频道「'+b.dataset.del+'」。此操作无法撤销。')) return;
    await api('/v1/admin/channels/'+encodeURIComponent(b.dataset.del), { method:'DELETE' });
    toast('频道已删除'); refreshAll();
  });
  tb.querySelectorAll('[data-probe]').forEach(b => b.onclick = async () => {
    try {
      const r = await api('/v1/admin/channels/'+encodeURIComponent(b.dataset.probe)+'/probe', { method:'POST', body:'{}' });
      toast(r.ok ? ('检测成功 · '+r.status+' · '+r.dur_ms+' ms') : ('检测失败：'+(r.error||'')));
    } catch(e) { toast(e.message); }
  });
  const tbody = $('tokBody'); tbody.innerHTML = '';
  (toks.access_tokens||[]).forEach(t => {
    const tr = document.createElement('tr');
    const stt = t.revoked_at ? '<span class="bad">已吊销</span>' : (t.enabled?'<span class="ok">有效</span>':'已停用');
    tr.innerHTML = '<td>'+esc(t.name)+'</td><td class="mono">'+esc(t.token_prefix)+'…</td><td class="mono">'+esc(t.scope)+
      '</td><td>'+stt+'</td><td><button class="secondary" data-rev="'+t.id+'">吊销</button> <button class="danger" data-tdel="'+t.id+'">删除</button></td>';
    tbody.appendChild(tr);
  });
  tbody.querySelectorAll('[data-rev]').forEach(b => b.onclick = async () => {
    await api('/v1/admin/access-tokens/'+b.dataset.rev+'/revoke', { method:'POST', body:'{}' });
    toast('令牌已吊销'); refreshAll();
  });
  tbody.querySelectorAll('[data-tdel]').forEach(b => b.onclick = async () => {
    await api('/v1/admin/access-tokens/'+b.dataset.tdel, { method:'DELETE' });
    toast('令牌已删除'); refreshAll();
  });
  $('setBase').value = set.public_base_url || '';
  $('egDefault').value = eg.default || 'direct';
  $('egPolicy').value = eg.playlist_policy || 'rewrite';
  $('egProxies').textContent = (eg.proxies||[]).map(p => p.id + ' → ' + p.url + (p.disabled?' [停用]':'')).join('\\n') || '暂无代理配置';
  const erb = $('egRules'); erb.innerHTML = '';
  (eg.rules||[]).forEach(r => {
    const tr = document.createElement('tr');
    const pid = r.proxy || r.proxy_id || '';
    tr.innerHTML = '<td class="mono">'+esc(r.id||'')+' p'+(r.priority??'')+'</td><td class="mono">'+esc(r.kind)+' '+esc(r.pattern)+
      '</td><td class="mono">'+esc(pid)+'</td><td><button class="danger" data-rdel="'+esc(r.id)+'">删除</button></td>';
    erb.appendChild(tr);
  });
  if (!(eg.rules||[]).length) erb.innerHTML = '<tr><td colspan="4" class="muted">暂无规则</td></tr>';
  erb.querySelectorAll('[data-rdel]').forEach(b => b.onclick = async () => {
    await api('/v1/admin/egress/rules/'+encodeURIComponent(b.dataset.rdel), { method:'DELETE' });
    toast('规则已删除'); refreshAll();
  });
  await loadLogs();
}
$('btnEgSaveMeta').onclick = async () => {
  try {
    await api('/v1/admin/egress', { method:'PUT', body: JSON.stringify({
      default: $('egDefault').value.trim() || 'direct',
      playlist_policy: $('egPolicy').value
    })});
    toast('策略已生效'); refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnEgSavePx').onclick = async () => {
  try {
    await api('/v1/admin/egress/proxies', { method:'POST', body: JSON.stringify({
      id: $('egPxId').value.trim(), name: $('egPxName').value.trim(), url: $('egPxURL').value.trim()
    })});
    toast('代理已存储'); refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnEgSaveRule').onclick = async () => {
  try {
    await api('/v1/admin/egress/rules', { method:'POST', body: JSON.stringify({
      id: $('egRuleId').value.trim(),
      priority: Number($('egRulePri').value||100),
      kind: $('egRuleKind').value,
      pattern: $('egRulePat').value.trim(),
      proxy: $('egRuleProxy').value.trim() || 'direct'
    })});
    toast('规则已生效'); refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnEgTest').onclick = async () => {
  try {
    const r = await api('/v1/admin/egress/test', { method:'POST', body: JSON.stringify({
      url: $('egURL').value.trim(), channel_id: $('egCh').value.trim()
    })});
    $('egResult').textContent = JSON.stringify(r, null, 2);
    toast(r.ok ? ('测试成功 · '+r.via_proxy+' · '+r.dur_ms+' ms') : ('测试失败：'+(r.error||'')));
  } catch(e) { toast(e.message); }
};
async function loadLogs() {
  try {
    const data = await api('/v1/admin/access-logs?limit=80');
    const tb = $('logBody'); tb.innerHTML = '';
    (data.access_logs||[]).forEach(l => {
      const tr = document.createElement('tr');
      tr.innerHTML = '<td class="muted">'+esc(fmtTime(l.created_at))+'</td><td class="mono">'+esc(l.token_prefix)+'</td><td class="mono">'+esc(l.path)+
        '</td><td class="mono">'+esc(l.channel_id||'—')+'</td><td>'+l.status+'</td><td class="muted">'+esc(l.remote||'')+'</td>';
      tb.appendChild(tr);
    });
    if (!(data.access_logs||[]).length) {
      tb.innerHTML = '<tr><td colspan="6" class="muted">暂无记录</td></tr>';
    }
  } catch(e) {}
}
$('btnLogin').onclick = () => login().catch(e => toast(e.message));
$('btnLogout').onclick = logout;
$('btnRefresh').onclick = () => refreshAll().catch(e => toast(e.message));
$('btnLogs').onclick = () => loadLogs().catch(e => toast(e.message));
$('btnSaveCh').onclick = async () => {
  try {
    const body = {
      id: $('chId').value.trim(), title: $('chTitle').value.trim(), group: $('chGroup').value.trim(),
      upstream: $('chUp').value, path: $('chPath').value.trim(), ingress: $('chIngress').value,
      keys_file: $('chKeys').value.trim(), prefer_height: Number($('chPref').value||0),
      on_demand: true, disabled: false
    };
    await api('/v1/admin/channels', { method:'POST', body: JSON.stringify(body) });
    toast('频道已存储'); refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnImpPreview').onclick = async () => {
  try {
    const data = await api('/v1/admin/import/m3u', { method:'POST', body: JSON.stringify({
      content: $('impText').value,
      default_upstream: $('impUp').value,
      default_keys_file: $('impKeys').value.trim(),
      prefer_height: Number($('impPref').value||0),
      apply: false
    })});
    state.preview = data.entries || [];
    $('btnImpApply').disabled = !state.preview.length;
    const lines = state.preview.slice(0, 40).map(e =>
      (e.skip?'[跳过] ':'') + (e.suggested_id||'?') + ' ← ' + (e.title||'') + ' → ' + (e.suggested_path||'') + ' ('+(e.suggested_ingress||'')+') ' + (e.note||'')
    );
    $('impPrev').textContent = '共 '+state.preview.length+' 项\n' + lines.join('\n') + (state.preview.length>40?'\n…':'');
    toast('预览已完成');
  } catch(e) { toast(e.message); }
};
$('btnImpApply').onclick = async () => {
  if (!state.preview || !state.preview.length) return;
  if (!confirm('将导入 '+state.preview.length+' 个频道。继续？')) return;
  try {
    const data = await api('/v1/admin/import/m3u', { method:'POST', body: JSON.stringify({
      entries: state.preview,
      default_upstream: $('impUp').value,
      default_keys_file: $('impKeys').value.trim(),
      prefer_height: Number($('impPref').value||0),
      apply: true
    })});
    toast('导入完成：已写入 '+data.created+' 项，已跳过 '+data.skipped+' 项');
    state.preview = null; $('btnImpApply').disabled = true;
    refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnNewTok').onclick = async () => {
  try {
    const scope = $('tokScope').value.trim();
    const channel_ids = scope ? scope.split(/[,\s]+/).filter(Boolean) : [];
    const data = await api('/v1/admin/access-tokens', { method:'POST', body: JSON.stringify({
      name: $('tokName').value.trim() || 'link', note: $('tokNote').value.trim(), channel_ids
    })});
    $('tokOnce').classList.remove('hidden');
    $('tokPlain').textContent = data.token;
    $('tokURL').textContent = data.playlist_url;
    toast('访问链接已创建'); refreshAll();
  } catch(e) { toast(e.message); }
};
$('btnCopyTok').onclick = () => navigator.clipboard.writeText($('tokPlain').textContent).then(()=>toast('令牌已拷贝'));
$('btnCopyURL').onclick = () => navigator.clipboard.writeText($('tokURL').textContent).then(()=>toast('播放列表地址已拷贝'));
$('btnSaveSet').onclick = async () => {
  try {
    await api('/v1/admin/settings', { method:'PUT', body: JSON.stringify({ public_base_url: $('setBase').value.trim() }) });
    toast('设置已存储'); refreshAll();
  } catch(e) { toast(e.message); }
};
fetch('/',{headers:{Accept:'application/json'},cache:'no-store'}).then(r=>r.json()).then(j => { $('ver').textContent = j.version||''; }).catch(()=>{});
if (state.token) {
  api('/v1/me').then(me => { $('who').textContent = me.username+' · '+me.role; showApp(true); return refreshAll(); }).catch(() => logout());
} else { showApp(false); }
</script>
</body>
</html>
`