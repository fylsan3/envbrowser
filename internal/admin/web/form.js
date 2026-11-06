'use strict';
// 新建 / 编辑 / 复制环境，以及环境详情。与 app.js 拼接后一起下发。

// 一个代理的输入组（类型、地址、端口、用户名、密码）
function proxyFields(up, locked) {
  up = up || {};
  const f = {
    type: h('select', { disabled: locked }, h('option', { value: '', text: '（无）' }), h('option', { value: 'socks5h', text: 'socks5h' }), h('option', { value: 'http', text: 'http' })),
    host: h('input', { value: up.host || '', disabled: locked, placeholder: 'x.x.x.x' }),
    port: h('input', { type: 'number', value: up.port || '', disabled: locked }),
    user: h('input', { value: up.username || '', disabled: locked, autocomplete: 'off' }),
    pass: h('input', { type: 'password', value: up.password || '', disabled: locked, autocomplete: 'new-password' }),
  };
  f.type.value = up.type || '';
  f.value = () => f.type.value ? { type: f.type.value, host: f.host.value.trim(), port: +f.port.value, username: f.user.value, password: f.pass.value } : null;
  return f;
}

// 默认隐藏的密码框 + “显示”按钮
function pwBox(input) {
  const b = h('button', { type: 'button', text: '显示' });
  b.addEventListener('click', () => {
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    b.textContent = show ? '隐藏' : '显示';
  });
  return h('div.pw', {}, input, b);
}

function proxyGrid(title, f, primary) {
  if (primary) f.type.firstChild.remove(); // 主代理必填，去掉“无”
  return [h('h3', { text: title }), h('div.g', {},
    h('div', {}, h('label', { text: '类型' }), f.type),
    h('div', {}, h('label', { text: '地址' }), f.host),
    h('div', {}, h('label', { text: '端口' }), f.port),
    h('div', {}),
    h('div', {}, h('label', { text: '用户名' }), f.user),
    h('div', {}, h('label', { text: '密码' }), pwBox(f.pass)))];
}

const MODULE_NAMES = {
  isolation: '隔离', network_guard: '网络守护', fingerprint: '指纹', timezone: '时区', language: '语言',
  window: '窗口', webrtc: 'WebRTC', ua_client_hints: 'UA 提示', hardware: '硬件', canvas: 'Canvas',
  audio: '音频', fonts: '字体', clientrects: 'ClientRects', gpu: '显卡（关 = 显示真机型号）',
};

routes.new = () => envForm('new');
routes.edit = id => envForm('edit', id);
routes.copy = id => envForm('copy', id);

async function envForm(mode, id) {
  let env = null, row = null;
  if (id) ({ env, row } = await api('GET', '/api/envs/' + id));
  const running = mode === 'edit' && row.status !== 'closed';
  const locked = running; // 运行中：代理与指纹只读
  // 复制：账号与代理不复制（需求第 11 节），其余沿用
  const src = mode === 'copy' ? null : env;

  const name = h('input', { value: mode === 'copy' ? env.name + ' 副本' : (env ? env.name : '') });
  const acUser = h('input', { value: src ? src.account.username : '', autocomplete: 'off' });
  const acPass = h('input', { type: 'password', value: src ? src.account.password : '', autocomplete: 'new-password' });
  const acNote = h('input', { value: src ? src.account.note : '' });
  const p1 = proxyFields(src && src.proxy.primary, locked);
  const p2 = proxyFields(src && src.proxy.secondary, locked);
  const active = h('select', { disabled: locked }, h('option', { value: 'primary', text: '主代理' }), h('option', { value: 'secondary', text: '备用代理' }));
  active.value = (src && src.proxy.active) || 'primary';
  const exitIP = h('input', { value: src ? src.proxy.exit_ip : '', disabled: locked, placeholder: '留空 = 主代理地址' });
  let exitCountry = src ? (src.proxy.exit_country || '') : '';
  const tz = h('select', { disabled: locked }, META.zones.map(z => h('option', { value: z.tz, text: `${z.label}（${z.tz}）` })));
  const curTZ = env ? env.fingerprint.timezone : 'Asia/Tokyo';
  if (!META.zones.some(z => z.tz === curTZ)) tz.append(h('option', { value: curTZ, text: curTZ }));
  tz.value = curTZ;
  const lang = h('input', { value: env ? (env.fingerprint.languages || [])[0] || '' : '', disabled: locked, placeholder: '留空 = 按时区默认' });
  tz.addEventListener('change', () => { lang.value = (META.zones.find(z => z.tz === tz.value) || {}).language || ''; });
  const mods = env ? env.modules : META.modules;
  const boxes = {};
  // 按草图顺序排列；万一后端多了新积木，排在最后
  const order = [...Object.keys(MODULE_NAMES).filter(k => k in META.modules),
    ...Object.keys(META.modules).filter(k => !(k in MODULE_NAMES))];
  const chips = h('div.chips', {}, order.map(k => {
    boxes[k] = h('input', { type: 'checkbox', checked: mods[k] !== undefined ? mods[k] : META.modules[k], disabled: locked });
    return h('label', {}, boxes[k], MODULE_NAMES[k] || k);
  }));
  const checkOut = h('div.mut');

  const testBtn = button('测试代理', async () => {
    checkOut.textContent = '检测中（最多 60 秒）…';
    const c = await api('POST', '/api/proxy/check', { proxy: p1.value(), exit_ip: exitIP.value });
    checkOut.textContent = checkLines(c).join('　');
    if (c.country) exitCountry = c.country;
  });
  const zoneBtn = button('按出口国家自动匹配', async () => {
    const r = await api('POST', '/api/proxy/autozone', { proxy: p1.value() });
    exitCountry = r.country;
    if (!r.zones || !r.zones.length) { toast(`出口国家 ${r.country} 不在时区列表中，请手动选择`); return; }
    if (r.zones.length > 1 && !r.zones.some(z => z.tz === tz.value)) {
      toast(`出口国家 ${r.country} 有多个时区：${r.zones.map(z => z.label).join('、')}。已选第一个，可再手动改。`, 8000);
    }
    if (!r.zones.some(z => z.tz === tz.value)) tz.value = r.zones[0].tz;
    lang.value = (r.zones.find(z => z.tz === tz.value) || r.zones[0]).language;
    toast(`出口国家 ${r.country} → ${zoneLabel(tz.value)}`);
  });
  if (locked) { testBtn.disabled = true; zoneBtn.disabled = true; }

  const body = () => {
    const modules = {};
    for (const [k, b] of Object.entries(boxes)) modules[k] = b.checked;
    return {
      name: name.value, account: { username: acUser.value, password: acPass.value, note: acNote.value },
      primary: p1.value(), secondary: p2.value(), active: active.value,
      exit_ip: exitIP.value, exit_country: mode === 'edit' ? '' : exitCountry,
      timezone: tz.value, language: lang.value, modules,
      copy_from: mode === 'copy' ? id : '',
    };
  };
  const save = button(mode === 'edit' ? '保存' : '创建', async () => {
    const b = body();
    if (mode === 'edit') { delete b.copy_from; delete b.exit_country; await api('POST', '/api/envs/' + id, b); toast('已保存'); }
    else { const r = await api('POST', '/api/envs', b); toast(`已创建 ${r.id}`); }
    go('list');
  }, 'pri');

  const head = h('div.bar', {}, h('b', { text: { new: '新建环境', edit: '编辑环境 ' + id, copy: '复制环境（来源 ' + id + '）' }[mode] }));
  if (mode === 'edit') {
    head.append(h('span.mut', { text: `模板 ${env.fingerprint.template} · 种子 ${env.fingerprint.seed.fingerprint}（自动分配）` }));
    const regen = button('重新生成种子', async () => {
      if (!confirm('重新生成种子后，这个环境的 Canvas、音频等指纹都会变，网站会把它当成一台新设备。确定？')) return;
      await api('POST', `/api/envs/${id}/regen-seed`); toast('已重新生成'); envForm(mode, id);
    });
    regen.disabled = locked;
    head.append(h('span', { style: 'margin-left:auto' }, regen));
  } else {
    head.append(h('span.mut', { text: mode === 'copy'
      ? '复制账号与代理以外的配置；种子重新生成，浏览器数据不复制。请填写新的账号和代理。'
      : '模板与种子创建时自动分配。' }));
  }

  view().replaceChildren(h('div.card', {},
    head,
    running ? h('div.warn', { text: '环境运行中：代理和指纹只读，只能改名称和账号。关闭环境后才能修改其余字段。' }) : null,
    h('h3', { text: '账号' }),
    h('div.g', {},
      h('div', {}, h('label', { text: '名称' }), name),
      h('div', {}, h('label', { text: '登录账号' }), acUser),
      h('div', {}, h('label', { text: '登录密码' }), pwBox(acPass)),
      h('div', {}, h('label', { text: '备注' }), acNote)),
    proxyGrid('主代理', p1, true),
    h('div.g', { style: 'margin-top:10px' },
      h('div', {}, h('label', { text: '指定出口 IP' }), exitIP),
      h('div.span2', { style: 'align-self:end' }, testBtn),
      h('div', {}),
      h('div.span4', {}, checkOut)),
    proxyGrid('备用代理（可选，只能在环境关闭时手动切换）', p2, false),
    h('div.g', { style: 'margin-top:10px' }, h('div', {}, h('label', { text: '当前使用' }), active)),
    h('h3', { text: '时区与语言' }),
    h('div.g', {},
      h('div.span2', {}, h('label', { text: '时区' }), tz),
      h('div', {}, h('label', { text: '语言' }), lang),
      h('div', { style: 'align-self:end' }, zoneBtn)),
    h('h3', { text: '积木开关' }),
    chips,
    h('div', { style: 'margin-top:16px' }, save, button('取消', () => go('list')))));
}

// ---------- 详情 ----------
routes.detail = async id => {
  const { env, row } = await api('GET', '/api/envs/' + id);
  const log = await api('GET', `/api/envs/${id}/log`);
  const reports = await api('GET', `/api/envs/${id}/reports`);
  const up = env.proxy.primary;
  const kv = (k, v) => h('tr', {}, h('th', { text: k, style: 'width:160px' }), h('td', { text: v === '' || v == null ? '—' : String(v) }));
  const pass = h('input', { type: 'password', value: env.account.password, readOnly: true });
  const ppass = h('input', { type: 'password', value: up.password, readOnly: true });
  const off = Object.entries(env.modules).filter(([, v]) => !v).map(([k]) => MODULE_NAMES[k] || k);
  view().replaceChildren(
    h('div.card', {},
      h('div.bar', {}, h('b', { text: `${env.id} · ${env.name}` }), h('span.pill.' + row.status, { text: STATUS[row.status] }),
        h('span', { style: 'margin-left:auto' }, row.status === 'closed' ? button('编辑', () => go('edit/' + id)) : null, button('返回', () => go('list')))),
      row.last_err ? h('div.err', { text: row.last_err }) : null,
      h('table', {},
        kv('本次运行本地端口', row.port ? '127.0.0.1:' + row.port : ''),
        kv('最近出口 IP', row.last_ip),
        kv('登录账号', env.account.username),
        h('tr', {}, h('th', { text: '登录密码' }), h('td', {}, pwBox(pass))),
        kv('备注', env.account.note),
        kv('主代理', `${up.type}://${up.username}@${up.host}:${up.port}`),
        h('tr', {}, h('th', { text: '主代理密码' }), h('td', {}, pwBox(ppass))),
        kv('备用代理', env.proxy.secondary ? `${env.proxy.secondary.type}://${env.proxy.secondary.username}@${env.proxy.secondary.host}:${env.proxy.secondary.port}` : ''),
        kv('当前使用', env.proxy.active === 'secondary' ? '备用代理' : '主代理'),
        kv('指定出口 / 国家', `${env.proxy.exit_ip} / ${env.proxy.exit_country || '未记录'}`),
        kv('时区 / 语言', `${zoneLabel(env.fingerprint.timezone)} / ${(env.fingerprint.languages || []).join(',')}`),
        kv('模板', `${env.fingerprint.template}（系统 ${env.fingerprint.platform_version}，${env.fingerprint.hardware_concurrency} 核）`),
        kv('指纹种子', env.fingerprint.seed.fingerprint),
        kv('窗口', `${env.fingerprint.seed.window.width} × ${env.fingerprint.seed.window.height}`),
        kv('已关闭的积木', off.join('、') || '无'),
        kv('浏览器版本', env.chromium_version),
        kv('创建时间', env.created_at))),
    reportCard(reports),
    h('div.card', {}, h('div.bar', {}, h('b', { text: '运行日志（含检测记录，最近 64KB）' }), h('span', { style: 'margin-left:auto' }, button('刷新', () => routes.detail(id)))),
      h('pre', { text: log || '还没有日志' })));
};

// 检测报告历史（需求规格第 9 节：reports/ 下按时间保存，可在管理页面查看）。
// 只列摘要；有未通过或需要留意的项时，点开看目标值与实际值。
function reportCard(rs) {
  const fmt = t => new Date(t).toLocaleString('zh-CN', { hour12: false });
  const tbody = h('tbody', {}, rs.map(r => {
    const tr = h('tr', {},
      h('td', { text: fmt(r.time) }),
      h('td', { text: r.seed }),
      h('td', {}, h(r.failed ? 'span.err' : 'span', { text: r.summary })),
      h('td', { text: r.file }));
    if (!r.bad.length) return tr;
    const det = h('details', {}, h('summary', { text: `${r.bad.length} 项需要看一眼` }),
      h('table', {}, r.bad.map(b => h('tr', {},
        h('td', { text: (b.ok ? '△ ' : '✗ ') + b.item }),
        h('td.mut', { text: '目标 ' + (b.want || '—') }),
        h('td', { text: '实际 ' + (b.got || '—') }),
        h('td.mut', { text: b.where || '' })))));
    tr.lastChild.append(det);
    return tr;
  }));
  return h('div.card', {},
    h('div.bar', {}, h('b', { text: `检测报告历史（${rs.length} 份，最新的在前）` })),
    rs.length
      ? h('table', {}, h('thead', {}, h('tr', {}, ['时间', '种子', '结果', '文件'].map(t => h('th', { text: t })))), tbody)
      : h('div.mut', { text: '还没有检测报告。测试模式下打开检测页，或运行 envbrowser fpcheck，报告会保存到环境目录的 reports/ 下。' }));
}
