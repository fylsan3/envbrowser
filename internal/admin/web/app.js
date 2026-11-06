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

function button(text, fn, cls = '') {
  const b = h('button' + (cls ? '.' + cls : ''), { text });
  b.addEventListener('click', act(b, fn));
  return b;
}

let META = null;
let timer = null;

const STATUS = { closed: '未打开', starting: '启动中', running: '运行中', blocked: '已断网' };

function zoneLabel(tz) {
  const z = (META.zones || []).find(z => z.tz === tz);
  return z ? z.label : tz;
}

// ---------- 路由 ----------
const routes = {};
function go(hash) { location.hash = hash; }
async function render() {
  clearInterval(timer);
  const [name, arg] = location.hash.slice(1).split('/');
  const page = name || 'list';
  document.querySelectorAll('header a').forEach(a => a.classList.toggle('on', a.dataset.nav === page));
  try {
    await (routes[page] || routes.list)(arg);
  } catch (e) {
    view().replaceChildren(h('div.card', {}, h('div.err', { text: e.message })));
  }
}
window.addEventListener('hashchange', render);
document.querySelectorAll('[data-nav]').forEach(el =>
  el.addEventListener('click', () => go(el.dataset.nav)));

// ---------- 环境列表 ----------
let filterText = '', filterStatus = '';
routes.list = async () => {
  const search = h('input', { placeholder: '搜索名称、账号、备注、代理地址', value: filterText });
  const sel = h('select', {},
    h('option', { value: '', text: '全部状态' }),
    Object.entries(STATUS).map(([k, v]) => h('option', { value: k, text: v })));
  sel.value = filterStatus;
  const tbody = h('tbody');
  const card = h('div.card', {},
    h('div.bar', {}, search, sel),
    h('table', {},
      h('thead', {}, h('tr', {}, ['ID', '名称 / 账号', '代理', '出口', '时区', '状态', '操作'].map(t => h('th', { text: t })))),
      tbody));
  view().replaceChildren(card);

  let rows = [];
  const draw = () => {
    const q = filterText.toLowerCase();
    const list = rows.filter(r =>
      (!filterStatus || r.status === filterStatus) &&
      (!q || [r.id, r.name, r.account, r.note, r.proxy_addr].some(x => (x || '').toLowerCase().includes(q))));
    tbody.replaceChildren(...list.map(rowEl));
    if (!list.length) tbody.append(h('tr', {}, h('td.mut', { colSpan: 7, text: rows.length ? '没有符合条件的环境' : '还没有环境，点右上角“新建环境”' })));
  };
  search.addEventListener('input', () => { filterText = search.value; draw(); });
  sel.addEventListener('change', () => { filterStatus = sel.value; draw(); });
  const load = async () => { rows = await api('GET', '/api/envs'); draw(); memWarn(rows); };
  await load();
  timer = setInterval(() => { if (!document.activeElement || document.activeElement.tagName !== 'BUTTON') load().catch(() => {}); }, 3000);
};

// 同时打开（含启动中）的环境达到设置值时提示内存，不拦截（需求规格第 6 节）
function memWarn(rows) {
  const n = rows.filter(r => r.status !== 'closed').length;
  const max = META.memory_warn_n || 0;
  const el = $('#memwarn');
  el.hidden = !(max > 0 && n > max);
  el.textContent = `已打开 ${n} 个环境（超过提示阈值 ${max}）。每个浏览器约占 300MB–1GB 内存，电脑变慢时请关掉暂时不用的环境。阈值可在“设置”里修改。`;
}

function rowEl(r) {
  const ops = [];
  if (r.status === 'closed') {
    ops.push(button('打开', async () => { await api('POST', `/api/envs/${r.id}/open`); toast('正在校验出口并启动，结果见状态栏'); routes.list(); }, 'pri'));
    ops.push(button('编辑', () => go('edit/' + r.id)));
  } else {
    ops.push(button(r.status === 'starting' ? '取消' : '关闭', async () => { await api('POST', `/api/envs/${r.id}/close`); routes.list(); }));
  }
  ops.push(button('检测', async () => { toast('检测中…', 60000); showCheck(await api('POST', `/api/envs/${r.id}/check`)); }));
  ops.push(button('详情', () => go('detail/' + r.id)));
  ops.push(button('复制', () => go('copy/' + r.id)));
  if (r.status === 'closed') ops.push(button('删除', () => trashEnv(r), 'danger'));
  return h('tr', {},
    h('td', { text: r.id }),
    h('td', {}, r.name, h('div.mut', { text: r.account || '—' })),
    h('td', {}, r.proxy_type, h('div.mut', { text: r.proxy_addr })),
    h('td', {}, r.exit_ip, h('div.mut', { text: (r.exit_country || '国家未记录') + (r.last_ip && r.last_ip !== r.exit_ip ? ' · 最近 ' + r.last_ip : '') })),
    h('td', { text: zoneLabel(r.timezone) }),
    h('td', {}, h('span.pill.' + r.status, { text: STATUS[r.status] || r.status }),
      r.port ? h('div.mut', { text: '端口 ' + r.port }) : null,
      r.last_err ? h('div.err', { text: r.last_err }) : null),
    h('td', {}, ops));
}

async function trashEnv(r) {
  if (!confirm(`删除环境 ${r.id}（${r.name}）？\n整个目录（含浏览器数据、Cookie）会移到废纸篓，可从废纸篓恢复。`)) return;
  if (!confirm(`再次确认：删除 ${r.id}？`)) return;
  const res = await api('POST', `/api/envs/${r.id}/trash`);
  toast('已移到废纸篓：' + res.moved_to, 8000);
  routes.list();
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
function showCheck(c) { toast(checkLines(c).join('\n'), 12000); }

// ---------- 模板 ----------
routes.templates = async () => {
  const ts = await api('GET', '/api/templates');
  const tbody = h('tbody', {}, ts.map(t => h('tr', {},
    h('td', {}, t.name, t.source ? h('div.mut', {}, h('a', { href: t.source, target: '_blank', rel: 'noreferrer', text: '来源' })) : null),
    h('td', { text: t.model || '—' }),
    h('td', { text: t.chip || '—' }),
    h('td', { text: t.hardware_concurrency }),
    h('td', { text: t.platform_version }),
    h('td', { text: t.used }),
    h('td', {}, button(t.enabled ? '停用' : '启用', async () => {
      await api('POST', '/api/templates/enable', { name: t.name, enabled: !t.enabled });
      routes.templates();
    }), t.enabled ? null : h('span.mut', { text: ' 已停用' }),
    t.used ? null : button('删除', async () => {
      if (!confirm(`删除模板“${t.name}”？没有环境在用它。删除后不能恢复，需要时可再手动新增。`)) return;
      await api('POST', '/api/templates/delete', { name: t.name });
      toast('已删除'); routes.templates();
    }, 'danger')))));
  const f = { name: h('input'), model: h('input'), chip: h('input'), hc: h('input', { type: 'number', min: 1 }), pv: h('input', { placeholder: '如 15.1.0' }) };
  const add = button('新增模板', async () => {
    await api('POST', '/api/templates', { name: f.name.value, model: f.model.value, chip: f.chip.value,
      hardware_concurrency: +f.hc.value, platform_version: f.pv.value.trim() });
    toast('已新增'); routes.templates();
  }, 'pri');
  view().replaceChildren(
    h('div.card', {},
      h('div.mut', { text: '新建环境时从已启用的模板中选使用次数最少的。停用不影响已绑定的环境。有环境在用的模板不能删除（可以停用）。网页能读到的只有系统版本和 CPU 核数。' }),
      h('table', {}, h('thead', {}, h('tr', {}, ['名称', '机型', '芯片', '核数', '系统版本', '使用中', ''].map(t => h('th', { text: t })))), tbody)),
    h('div.card', {}, h('h3', { text: '手动新增' }),
      h('div.g', {},
        h('div', {}, h('label', { text: '名称 *' }), f.name),
        h('div', {}, h('label', { text: '机型' }), f.model),
        h('div', {}, h('label', { text: '芯片' }), f.chip),
        h('div', {}),
        h('div', {}, h('label', { text: 'CPU 核数 *' }), f.hc),
        h('div', {}, h('label', { text: '系统版本 *' }), f.pv)),
      h('div', { style: 'margin-top:12px' }, add)));
};

// ---------- 设置 ----------
routes.settings = async () => {
  const s = await api('GET', '/api/settings');
  const f = {
    chromium_path: h('input', { value: s.chromium_path }),
    chromium_version: h('input', { value: s.chromium_version }),
    port_min: h('input', { type: 'number', value: s.port_min }),
    port_max: h('input', { type: 'number', value: s.port_max }),
    memory_warn_n: h('input', { type: 'number', value: s.memory_warn_n }),
    test_mode: h('input', { type: 'checkbox', checked: s.test_mode, style: 'width:auto' }),
    test_target: h('input', { value: s.test_target }),
  };
  const save = button('保存', async () => {
    await api('POST', '/api/settings', {
      root: s.root, admin_addr: s.admin_addr,
      chromium_path: f.chromium_path.value, chromium_version: f.chromium_version.value,
      port_min: +f.port_min.value, port_max: +f.port_max.value, memory_warn_n: +f.memory_warn_n.value,
      test_mode: f.test_mode.checked, test_target: f.test_target.value,
    });
    await loadMeta();
    toast('已保存。新设置对之后打开的环境生效。');
  }, 'pri');
  view().replaceChildren(h('div.card', {},
    h('div.g', {},
      h('div.span2', {}, h('label', { text: '数据根目录（只读，用环境变量 ENVBROWSER_ROOT 修改）' }), h('input', { value: s.root, disabled: true })),
      h('div.span2', {}, h('label', { text: '管理页面地址（只读）' }), h('input', { value: s.admin_addr, disabled: true })),
      h('div.span2', {}, h('label', { text: '浏览器（fingerprint-chromium）路径' }), f.chromium_path),
      h('div', {}, h('label', { text: '浏览器版本' }), f.chromium_version),
      h('div', {}),
      h('div', {}, h('label', { text: '中转端口起' }), f.port_min),
      h('div', {}, h('label', { text: '中转端口止' }), f.port_max),
      h('div', {}, h('label', { text: '同时打开多少个环境时提示内存' }), f.memory_warn_n),
      h('div', {}),
      h('div', {}, h('label', { text: '测试模式（放行一个本机检测页）' }), h('label', {}, f.test_mode, ' 开启')),
      h('div', {}, h('label', { text: '本机检测页地址' }), f.test_target)),
    h('div', { style: 'margin-top:14px' }, save)));
};

async function loadMeta() {
  META = await api('GET', '/api/meta');
  const tm = $('#testmode');
  tm.hidden = !META.test_mode;
  tm.textContent = `测试模式已开启：环境浏览器可以访问本机检测页 ${META.test_target}。用完请到“设置”里关闭。`;
}

window.addEventListener('DOMContentLoaded', async () => {
  try { await loadMeta(); } catch (e) { toast(e.message, 10000); return; }
  render();
});
