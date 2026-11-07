'use strict';
// 游龙管理页面。只用浏览器自带功能；所有文字一律经 textContent 写入，不拼 HTML。

const $ = s => document.querySelector(s);
const view = () => $('#view');

// h('tag.cls', {属性/事件}, 子节点...)
function h(spec, attrs, ...kids) {
  const [tag, ...cls] = spec.split('.');
  const el = document.createElement(tag || 'div');
  if (cls.length) el.className = cls.join(' ');
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'text') el.textContent = v;
    else if (k in el && typeof v !== 'string') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat()) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function toast(msg, ms = 4000) {
  const t = $('#toast');
  t.textContent = msg;
  t.style.display = 'block';
  clearTimeout(toast.t);
  toast.t = setTimeout(() => (t.style.display = 'none'), ms);
}

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const r = await fetch(path, opt);
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  const ct = r.headers.get('Content-Type') || '';
  if (r.status === 204) return null;
  return ct.includes('json') ? r.json() : r.text();
}

// 按钮点击期间禁用，出错弹提示
function act(btn, fn) {
  return async () => {
    btn.disabled = true;
    try { await fn(); } catch (e) { toast(e.message, 8000); } finally { btn.disabled = false; }
  };
}

// button('文字', 函数, 'pri sm') —— 样式类用空格分隔
function button(text, fn, cls = '') {
  const b = h('button.btn' + (cls ? '.' + cls.split(' ').join('.') : ''), { type: 'button', text });
  b.addEventListener('click', act(b, fn));
  return b;
}

// 页面顶部标题区
function pageTop(title, sub, ...right) {
  return h('div.top', {}, h('div', {}, h('h1', { text: title }), sub ? h('div.sub', { text: sub }) : null),
    h('span.sp'), ...right);
}

// 带图标的分区标题
function sec(icon, title, desc) {
  return h('div.sec', {}, h('div.ic', { text: icon }), h('div', {}, h('h4', { text: title }), desc ? h('p', { text: desc }) : null));
}

// 表单字段：<label class="f">标题 控件</label>
function field(label, ctl, hint) {
  return h('label.f', {}, label, ctl, hint ? h('div.hint', { text: hint }) : null);
}

// 开关样式的勾选（chip）。返回元素，el.checked 读写状态
function chip(text, checked, disabled, extra) {
  const el = h('span.chip', { tabIndex: 0 }, h('span.sw'), text, extra ? h('span.mute', { text: extra }) : null);
  const set = v => { el.checked = !!v; el.classList.toggle('on', el.checked); };
  set(checked);
  if (disabled) el.classList.add('dis');
  const flip = () => { if (!el.classList.contains('dis')) set(!el.checked); };
  el.addEventListener('click', flip);
  el.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); } });
  el.setDisabled = d => el.classList.toggle('dis', !!d);
  return el;
}

let META = null;
let timer = null;

const STATUS = { closed: '未打开', starting: '启动中', running: '运行中', blocked: '已断网' };
const AV_COLORS = ['#6366f1', '#0ea5e9', '#f59e0b', '#ef4444', '#10b981', '#8b5cf6', '#ec4899', '#14b8a6'];

// 彩色头像：名称最后一个字（“样本 A”显示 A），颜色按编号固定
function avatar(id, name) {
  const n = parseInt(String(id).replace(/\D/g, ''), 10) || 0;
  const s = Array.from((name || id || '?').trim());
  const ch = (s[s.length - 1] || '?').toUpperCase();
  return h('div.av', { style: 'background:' + AV_COLORS[n % AV_COLORS.length], text: ch });
}

// 模板显示名：机型名里已含芯片（如 “Mac mini (2023, M2)”）时不再重复芯片
function tplLabel(t) {
  const m = t.model || t.name, c = (t.chip || '').replace(/^Apple /, '');
  return c && !m.includes(c) ? m + ' · ' + c : m;
}

function pill(status) {
  return h('span.pill.' + status, { text: STATUS[status] || status });
}

function zoneLabel(tz) {
  const z = (META.zones || []).find(z => z.tz === tz);
  return z ? z.label.replace('（默认）', '') : tz;
}

// ---------- 路由 ----------
const routes = {};
function go(hash) { location.hash = hash; }
async function render() {
  clearInterval(timer);
  timer = null;
  const [name, arg] = location.hash.slice(1).split('/');
  const page = name || 'list';
  const navOf = { templates: 'templates', settings: 'settings' };
  document.querySelectorAll('.nav[data-nav]').forEach(a => a.classList.toggle('on', a.dataset.nav === (navOf[page] || 'list')));
  try {
    await (routes[page] || routes.list)(arg);
  } catch (e) {
    view().replaceChildren(h('div.card', {}, h('div.err', { text: e.message })));
  }
  window.scrollTo(0, 0);
}
window.addEventListener('hashchange', render);
document.querySelectorAll('[data-nav]').forEach(el =>
  el.addEventListener('click', () => go(el.dataset.nav)));

// 侧栏：环境数、运行中数、模板数
function sideCounts(rows) {
  $('#n-env').textContent = rows.length;
  $('#f-run').textContent = rows.filter(r => r.status === 'running').length + ' 个';
}
async function refreshSide() {
  try {
    const [rows, ts] = await Promise.all([api('GET', '/api/envs'), api('GET', '/api/templates')]);
    sideCounts(rows);
    $('#n-tpl').textContent = ts.length;
    memWarn(rows);
  } catch (e) { /* 侧栏刷新失败不打扰 */ }
}

// ---------- 环境列表 ----------
let filterText = '', filterStatus = '';
routes.list = async () => {
  const search = h('input', { placeholder: '搜索名称、账号、备注、代理地址', value: filterText });
  const seg = h('div.seg');
  const stats = h('div.stats');
  const tbody = h('tbody');
  view().replaceChildren(
    pageTop('环境', '一个环境 = 一个账号 = 一套指纹 = 一条代理', button('＋ 新建环境', () => go('new'), 'pri')),
    stats,
    h('div.card', {},
      h('div.bar', {}, h('div.search', {}, search), seg),
      h('table', {},
        h('thead', {}, h('tr', {}, ['环境', '代理 / 出口', '时区 · 机型', '状态'].map(t => h('th', { text: t })),
          h('th', { style: 'text-align:right', text: '操作' }))),
        tbody)));

  let rows = [], tpls = {};
  const stat = (k, v, color, small) => h('div.stat', {}, h('div.k', { text: k }),
    h('div.v', { style: color ? 'color:' + color : null }, String(v), small ? h('small', { text: ' ' + small }) : null));
  const draw = () => {
    const cnt = s => rows.filter(r => r.status === s).length;
    const max = META.memory_warn_n || 0;
    stats.replaceChildren(
      stat('全部环境', rows.length),
      stat('运行中', cnt('running'), 'var(--ok)', max ? '/ 提示阈值 ' + max : ''),
      stat('已断网', cnt('blocked'), 'var(--bad)'),
      stat('未打开', cnt('closed')));
    seg.replaceChildren(...[['', '全部 ' + rows.length], ...['running', 'starting', 'blocked', 'closed'].map(s => [s, STATUS[s] + ' ' + cnt(s)])]
      .map(([k, t]) => h('span' + (k === filterStatus ? '.on' : ''), { text: t, onclick: () => { filterStatus = k; draw(); } })));
    const q = filterText.toLowerCase();
    const list = rows.filter(r =>
      (!filterStatus || r.status === filterStatus) &&
      (!q || [r.id, r.name, r.account, r.note, r.proxy_addr].some(x => (x || '').toLowerCase().includes(q))));
    tbody.replaceChildren(...list.map(r => rowEl(r, tpls[r.template])));
    if (!list.length) tbody.append(h('tr', {}, h('td.mute', { colSpan: 5, style: 'text-align:center;padding:28px',
      text: rows.length ? '没有符合条件的环境' : '还没有环境，点右上角“＋ 新建环境”' })));
  };
  search.addEventListener('input', () => { filterText = search.value; draw(); });
  const load = async () => {
    rows = await api('GET', '/api/envs');
    draw(); memWarn(rows); sideCounts(rows);
  };
  const ts = await api('GET', '/api/templates');
  for (const t of ts) tpls[t.name] = t;
  $('#n-tpl').textContent = ts.length;
  await load();
  timer = setInterval(() => {
    const a = document.activeElement;
    // 正在点按钮或打开着“···”菜单时不刷新，免得菜单被重画掉
    if (a && (a.tagName === 'BUTTON' || a.closest('.more'))) return;
    if (document.querySelector('.more:hover')) return;
    load().catch(() => {});
  }, 3000);
};

// 同时打开（含启动中）的环境达到设置值时提示内存，不拦截（需求规格第 6 节）
function memWarn(rows) {
  const n = rows.filter(r => r.status !== 'closed').length;
  const max = META.memory_warn_n || 0;
  const el = $('#memwarn');
  el.hidden = !(max > 0 && n > max);
  el.textContent = `⚠ 已打开 ${n} 个环境（超过提示阈值 ${max}）。每个浏览器约占 300MB–1GB 内存，电脑变慢时请关掉暂时不用的环境。阈值可在“设置”里修改。`;
}

function openEnv(r, after) {
  return async () => { await api('POST', `/api/envs/${r.id}/open`); toast('正在校验出口并启动，结果见状态栏'); after(); };
}
function closeEnv(r, after) {
  return async () => { await api('POST', `/api/envs/${r.id}/close`); after(); };
}
async function checkEnv(r) {
  toast('检测中（最多 60 秒）…', 60000);
  showCheck(await api('POST', `/api/envs/${r.id}/check`));
}

function rowEl(r, tpl) {
  const ops = [];
  const again = () => routes.list();
  if (r.status === 'closed') ops.push(button('▶ 打开', openEnv(r, again), 'sm pri'));
  else ops.push(button(r.status === 'starting' ? '取消' : '关闭', closeEnv(r, again), 'sm'));
  ops.push(button('检测', () => checkEnv(r), 'sm'));
  const menu = h('div.menu', {},
    h('button', { type: 'button', text: '详情', onclick: () => go('detail/' + r.id) }),
    r.status === 'closed' ? h('button', { type: 'button', text: '编辑', onclick: () => go('edit/' + r.id) }) : null,
    h('button', { type: 'button', text: '复制', onclick: () => go('copy/' + r.id) }),
    r.status === 'closed' ? h('button.del', { type: 'button', text: '删除', onclick: () => trashEnv(r).catch(e => toast(e.message, 8000)) }) : null);
  ops.push(h('span.more', {}, h('button.btn.sm.ghost', { type: 'button', text: '···' }), menu));

  const exitLine = r.status === 'blocked'
    ? h('div.mute', { style: 'color:var(--bad)', text: r.last_err || '出口异常，已切断连接' })
    : h('div.mute', { text: '出口 ' + (r.exit_ip || '—') + ' · ' + (r.exit_country || '国家未记录') +
        (r.last_ip && r.last_ip !== r.exit_ip ? ' · 最近 ' + r.last_ip : '') });
  const model = tpl ? tplLabel(tpl) : r.template;
  return h('tr', {},
    h('td', {}, h('div.envname', { onclick: () => go('detail/' + r.id) }, avatar(r.id, r.name),
      h('div', {}, h('b', { text: r.name || r.id }), h('div.mute', { text: r.id + ' · ' + (r.account || '—') })))),
    h('td', {}, h('span.tag.blue', { text: r.proxy_type }), h('span.mono', { text: r.proxy_addr }), exitLine),
    h('td', {}, zoneLabel(r.timezone), h('div.mute', { text: model || '—' })),
    h('td', {}, pill(r.status),
      r.port ? h('div.mute', { text: '端口 ' + r.port }) : null,
      r.status === 'starting' ? h('div.mute', { text: '校验出口…' }) : null,
      r.last_err && r.status !== 'blocked' ? h('div.err', { text: r.last_err }) : null),
    h('td', {}, h('div.ops', {}, ops)));
}

async function trashEnv(r) {
  if (!confirm(`删除环境 ${r.id}（${r.name}）？\n整个目录（含浏览器数据、Cookie）会移到废纸篓，可从废纸篓恢复。`)) return;
  if (!confirm(`再次确认：删除 ${r.id}？`)) return;
  const res = await api('POST', `/api/envs/${r.id}/trash`);
  toast('已移到废纸篓：' + res.moved_to, 8000);
  go('list'); routes.list();
}

// 检测结果的文字版（列表页“检测”与编辑页“测试代理”共用）
function checkLines(c) {
  const out = [];
  out.push(c.exit_ok ? `✓ 出口 ${c.exit_ip}` : `✗ 出口校验未通过：${c.exit_err}`);
  out.push(c.country ? `✓ 出口国家 ${c.country}` : `✗ 未取得出口国家：${c.country_err}`);
  if (c.header_err) out.push(`✗ 明文 http 请求头检查失败：${c.header_err}`);
  else out.push(c.header_ok ? '✓ 明文 http 无暴露头' : `✗ 发现暴露头：${c.header_leaks.join(', ')}`);
  return out;
}
function checkOK(c) { return c.exit_ok && !!c.country && c.header_ok && !c.header_err; }
function showCheck(c) { toast(checkLines(c).join('\n'), 12000); }

// ---------- 模板 ----------
routes.templates = async () => {
  const ts = await api('GET', '/api/templates');
  $('#n-tpl').textContent = ts.length;
  const again = () => routes.templates();
  const cards = ts.map(t => {
    const isMini = /mini|studio/i.test(t.model || t.name);
    const side = h('div.side', {},
      button(t.enabled ? '停用' : '启用', async () => {
        await api('POST', '/api/templates/enable', { name: t.name, enabled: !t.enabled });
        again();
      }, t.enabled ? 'sm' : 'sm pri'),
      t.used ? null : button('删除', async () => {
        if (!confirm(`删除模板“${t.name}”？没有环境在用它。删除后不能恢复，需要时可再手动新增。`)) return;
        await api('POST', '/api/templates/delete', { name: t.name });
        toast('已删除'); again();
      }, 'sm danger'),
      t.source ? h('a', { href: t.source, target: '_blank', rel: 'noreferrer', text: '来源 ↗' }) : null);
    return h('div.tcard' + (t.enabled ? '' : '.off'), {},
      h('div.dev', { text: isMini ? '🖥' : '💻' }),
      h('div', { style: 'flex:1;min-width:0' },
        h('h5', {}, (t.model || t.name) + ' ', t.chip && !(t.model || '').includes(t.chip.replace(/^Apple /, '')) ? h('span.tag', { text: t.chip.replace(/^Apple /, '') }) : null,
          t.enabled ? null : h('span.tag', { style: 'background:#e2e8f0', text: '已停用' })),
        h('div.mute.mono', { text: t.name }),
        h('div', { style: 'margin-top:8px' },
          h('span.tag', { text: t.hardware_concurrency + ' 核' }),
          h('span.tag', { text: '系统 ' + t.platform_version }),
          h(t.used ? 'span.tag.blue' : 'span.tag', { text: t.used ? t.used + ' 个环境在用' : '未使用' }))),
      side);
  });
  view().replaceChildren(
    pageTop('模板', '新建环境时从已启用的模板里选使用次数最少的。网页能读到的只有系统版本和 CPU 核数',
      button('＋ 新增模板', async () => tplDialog(again), 'pri')),
    ts.length ? h('div.tpl', {}, cards) : h('div.card.mute', { text: '还没有模板。' }),
    h('div.hint', { style: 'margin-top:12px', text: '停用不影响已绑定的环境。有环境在用的模板不能删除（可以停用）。' }));
};

// 新增模板弹窗
function tplDialog(done) {
  const f = { name: h('input'), model: h('input', { placeholder: '如 MacBook Air 13' }), chip: h('input', { placeholder: '如 Apple M2' }),
    hc: h('input', { type: 'number', min: 1 }), pv: h('input', { placeholder: '如 15.1.0' }) };
  const dlg = h('dialog');
  const close = () => { dlg.close(); dlg.remove(); };
  dlg.append(
    h('h3', { text: '新增模板' }),
    h('div.grid.g2', {},
      field('名称 *', f.name), field('机型', f.model),
      field('芯片', f.chip), field('CPU 核数 *', f.hc),
      field('系统版本 *', f.pv)),
    h('div.actions', { style: 'margin-top:18px' },
      button('取消', close),
      button('新增', async () => {
        await api('POST', '/api/templates', { name: f.name.value.trim(), model: f.model.value.trim(), chip: f.chip.value.trim(),
          hardware_concurrency: +f.hc.value, platform_version: f.pv.value.trim() });
        close(); toast('已新增'); done();
      }, 'pri')));
  dlg.addEventListener('cancel', () => dlg.remove());
  document.body.append(dlg);
  dlg.showModal();
  f.name.focus();
}

// ---------- 设置 ----------
routes.settings = async () => {
  const s = await api('GET', '/api/settings');
  const f = {
    chromium_path: h('input', { value: s.chromium_path }),
    chromium_version: h('input', { value: s.chromium_version }),
    port_min: h('input', { type: 'number', value: s.port_min }),
    port_max: h('input', { type: 'number', value: s.port_max }),
    memory_warn_n: h('input', { type: 'number', value: s.memory_warn_n }),
    test_mode: chip('开启测试模式', s.test_mode),
    test_target: h('input', { value: s.test_target }),
  };
  const save = button('保存设置', async () => {
    await api('POST', '/api/settings', {
      root: s.root, admin_addr: s.admin_addr,
      chromium_path: f.chromium_path.value, chromium_version: f.chromium_version.value,
      port_min: +f.port_min.value, port_max: +f.port_max.value, memory_warn_n: +f.memory_warn_n.value,
      test_mode: f.test_mode.checked, test_target: f.test_target.value,
    });
    await loadMeta();
    routes.settings();
    toast('已保存。新设置对之后打开的环境生效。');
  }, 'pri');
  view().replaceChildren(
    pageTop('设置', '保存后对之后打开的环境生效'),
    h('div.card', {}, sec('◎', '浏览器', '游龙.app 默认用内置的 fingerprint-chromium'),
      h('div.grid', { style: 'grid-template-columns:2fr 1fr' },
        field('浏览器路径', f.chromium_path), field('版本', f.chromium_version))),
    h('div.card', {}, sec('⇄', '中转与资源', '每个环境占用一个本机端口'),
      h('div.grid.g3', {},
        field('中转端口起', f.port_min), field('中转端口止', f.port_max),
        field('同时打开多少个环境时提示内存', f.memory_warn_n))),
    h('div.card', {}, sec('⚑', '测试模式', '开启后放行一个本机检测页地址，页面顶部会有黄色提醒'),
      h('div.grid', { style: 'grid-template-columns:auto 1fr;align-items:end' },
        f.test_mode, field('本机检测页地址', f.test_target))),
    h('div.card', {}, sec('ⓘ', '关于', '只读'),
      h('dl.kv', {},
        h('dt', { text: '版本' }), h('dd', { text: '游龙 ' + (META.version || 'dev') }),
        h('dt', { text: '数据目录' }), h('dd.mono', { text: s.root }),
        h('dt', { text: '' }), h('dd.mute', { text: '用环境变量 ENVBROWSER_ROOT 修改' }),
        h('dt', { text: '管理页面' }), h('dd.mono', { text: s.admin_addr + '（只本机可访问）' }),
        h('dt', { text: '本机芯片' }), h('dd', { text: META.host_chip || '未识别' }))),
    h('div.actions', {}, save));
};

// 数据目录显示成 ~/xxx；太长时只留最后两级
function shortPath(p) {
  if (!p) return '';
  p = p.replace(/^\/Users\/[^/]+/, '~');
  const parts = p.split('/');
  return parts.length > 3 ? '…/' + parts.slice(-2).join('/') : p;
}

async function loadMeta() {
  META = await api('GET', '/api/meta');
  const tm = $('#testmode');
  tm.hidden = !META.test_mode;
  tm.textContent = `⚑ 测试模式已开启：环境浏览器可以访问本机检测页 ${META.test_target}。用完请到“设置”里关闭。`;
  $('#ver').textContent = '多环境浏览器 · ' + (META.version || 'dev');
  $('#f-ver').textContent = META.chromium_ver || '—';
  $('#f-ver').title = META.chromium_ver || '';
  $('#f-root').textContent = shortPath(META.root) || '—';
  $('#f-root').title = META.root || '';
}

window.addEventListener('DOMContentLoaded', async () => {
  try { await loadMeta(); } catch (e) { toast(e.message, 10000); return; }
  render();
  refreshSide();
  // 不在列表页时，侧栏数字也每 5 秒更新一次
  setInterval(() => { if (!timer) refreshSide(); }, 5000);
});

// ---------- 退出 ----------
$('#quit').addEventListener('click', async () => {
  if (!confirm('退出游龙？所有打开的环境都会被关闭。')) return;
  try { await api('POST', '/api/quit'); } catch (e) { toast(e.message, 8000); return; }
  clearInterval(timer);
  view().replaceChildren(h('div.card', {}, h('b', { text: '游龙已退出' }),
    h('p.mute', { text: '所有环境正在关闭。可以关掉这个页面；再次使用时双击 游龙.app。' })));
});
