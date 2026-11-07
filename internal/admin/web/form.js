'use strict';
// 新建 / 编辑 / 复制环境，以及环境详情。与 app.js 拼接后一起下发。

// 一个代理的输入组（类型、地址、端口、用户名、密码）
function proxyFields(up, locked) {
  up = up || {};
  const f = {
    type: h('select', { disabled: locked }, h('option', { value: '', text: '（无）' }), h('option', { value: 'socks5h', text: 'socks5h' }), h('option', { value: 'http', text: 'http' })),
    host: h('input', { value: up.host || '', disabled: locked, placeholder: 'x.x.x.x' }),
    port: h('input', { type: 'number', value: up.port || '', disabled: locked, placeholder: '1080' }),
    user: h('input', { value: up.username || '', disabled: locked, autocomplete: 'off', placeholder: '代理用户名' }),
    pass: h('input', { type: 'password', value: up.password || '', disabled: locked, autocomplete: 'new-password', placeholder: '代理密码' }),
  };
  f.type.value = up.type || '';
  f.value = () => f.type.value ? { type: f.type.value, host: f.host.value.trim(), port: +f.port.value, username: f.user.value, password: f.pass.value } : null;
  return f;
}

// 默认隐藏的密码框 + “显示”按钮
function pwBox(input) {
  const b = h('button.btn.sm', { type: 'button', text: '显示' });
  b.addEventListener('click', e => {
    e.preventDefault();
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    b.textContent = show ? '隐藏' : '显示';
  });
  return h('div.inwrap', {}, input, b);
}

// 主代理 / 备用代理的输入区（类型、地址、端口一行；用户名、密码一行）
function proxyGrid(f, primary, extra) {
  if (primary) f.type.firstChild.remove(); // 主代理必填，去掉“无”
  return [
    h('div.grid', { style: 'grid-template-columns:140px 1fr 120px' },
      field('类型', f.type), field('地址', f.host), field('端口', f.port)),
    h('div.grid.g3', { style: 'margin-top:14px' },
      field('用户名', f.user), field('密码', pwBox(f.pass)), extra || h('div')),
  ];
}

const MODULE_NAMES = {
  isolation: '隔离', network_guard: '网络守护', fingerprint: '指纹', timezone: '时区', language: '语言',
  window: '窗口', webrtc: 'WebRTC', ua_client_hints: 'UA 提示', hardware: '硬件', canvas: 'Canvas',
  audio: '音频', fonts: '字体', clientrects: 'ClientRects', gpu: '显卡',
};
const MODULE_EXTRA = { gpu: '（关 = 显示真机型号）' };

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
  const acUser = h('input', { value: src ? src.account.username : '', autocomplete: 'off', placeholder: 'user@example.com' });
  const acPass = h('input', { type: 'password', value: src ? src.account.password : '', autocomplete: 'new-password' });
  const acNote = h('input', { value: src ? src.account.note : '', placeholder: '可选' });
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
  const mods = env ? env.modules : META.modules;
  const boxes = {};
  // 按草图顺序排列；万一后端多了新积木，排在最后
  const order = [...Object.keys(MODULE_NAMES).filter(k => k in META.modules),
    ...Object.keys(META.modules).filter(k => !(k in MODULE_NAMES))];
  const chips = h('div.chips', {}, order.map(k =>
    (boxes[k] = chip(MODULE_NAMES[k] || k, mods[k] !== undefined ? mods[k] : META.modules[k], locked, MODULE_EXTRA[k]))));

  // 右侧预览
  const pv = {};
  const kv = h('dl.kv');
  const kvRow = (k, ...v) => { const dd = h('dd', {}, ...v); kv.append(h('dt', { text: k }), dd); return dd; };
  if (mode === 'edit') kvRow('编号', h('span.mono', { text: id }));
  else kvRow('编号', h('span.mute', { text: '创建时自动编号' }));
  if (mode === 'edit') kvRow('模板', env.fingerprint.template, h('div.mute', { text: `系统 ${env.fingerprint.platform_version} · ${env.fingerprint.hardware_concurrency} 核` }));
  else kvRow('模板', '自动分配', h('div.mute', { text: '轮换到使用最少的已启用模板' }));
  if (mode === 'edit') kvRow('指纹种子', h('span.mono', { text: String(env.fingerprint.seed.fingerprint) }));
  else kvRow('指纹种子', h('span.mute', { text: '自动生成，不与现有重复' }));
  pv.proxy = kvRow('代理');
  pv.tz = kvRow('时区');
  if (env && mode !== 'new') kvRow('窗口', `${env.fingerprint.seed.window.width} × ${env.fingerprint.seed.window.height}`);
  kvRow('浏览器', (env && mode === 'edit' && env.chromium_version) || META.chromium_ver || '—');
  let tested = null; // null 未测；true/false 测试结果
  const updPreview = () => {
    pv.proxy.replaceChildren(h('span.tag.blue', { text: p1.type.value || '—' }),
      tested === null ? h('span.mute', { text: '未测试' }) : tested ? '已测试通过' : h('span.err', { text: '测试未通过' }));
    pv.tz.textContent = zoneLabel(tz.value) + ' / ' + (lang.value || (META.zones.find(z => z.tz === tz.value) || {}).language || '—');
  };
  tz.addEventListener('change', () => { lang.value = (META.zones.find(z => z.tz === tz.value) || {}).language || ''; updPreview(); });
  lang.addEventListener('input', updPreview);
  p1.type.addEventListener('change', updPreview);
  updPreview();

  const checkOut = h('div', { hidden: true });
  const testBtn = button('测试代理', async () => {
    checkOut.hidden = false;
    checkOut.className = 'result wait';
    checkOut.textContent = '检测中（最多 60 秒）…';
    const t0 = Date.now();
    try {
      const c = await api('POST', '/api/proxy/check', { proxy: p1.value(), exit_ip: exitIP.value });
      tested = checkOK(c);
      checkOut.className = 'result ' + (tested ? 'ok' : 'bad');
      checkOut.textContent = checkLines(c).join('\n') + `\n用时 ${((Date.now() - t0) / 1000).toFixed(1)} 秒`;
      if (c.country) exitCountry = c.country;
    } catch (e) {
      tested = false;
      checkOut.className = 'result bad';
      checkOut.textContent = '✗ ' + e.message;
    }
    updPreview();
  }, 'sm');
  const zoneBtn = button('按出口国家自动匹配', async () => {
    const r = await api('POST', '/api/proxy/autozone', { proxy: p1.value() });
    exitCountry = r.country;
    if (!r.zones || !r.zones.length) { toast(`出口国家 ${r.country} 不在时区列表中，请手动选择`); return; }
    if (r.zones.length > 1 && !r.zones.some(z => z.tz === tz.value)) {
      toast(`出口国家 ${r.country} 有多个时区：${r.zones.map(z => z.label).join('、')}。已选第一个，可再手动改。`, 8000);
    }
    if (!r.zones.some(z => z.tz === tz.value)) tz.value = r.zones[0].tz;
    lang.value = (r.zones.find(z => z.tz === tz.value) || r.zones[0]).language;
    updPreview();
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
  const save = button(mode === 'edit' ? '保存' : '创建环境', async () => {
    const b = body();
    if (mode === 'edit') { delete b.copy_from; delete b.exit_country; await api('POST', '/api/envs/' + id, b); toast('已保存'); }
    else { const r = await api('POST', '/api/envs', b); toast(`已创建 ${r.id}`); }
    go('list');
  }, 'pri');

  const title = { new: '新建环境', edit: '编辑环境 ' + id, copy: '复制环境' }[mode];
  const sub = {
    new: '模板和指纹种子在创建时自动分配，各环境不重复',
    edit: `模板 ${env && env.fingerprint.template} · 种子 ${env && env.fingerprint.seed.fingerprint}（自动分配）`,
    copy: `来源 ${id}。复制账号与代理以外的配置；种子重新生成，浏览器数据不复制。请填写新的账号和代理。`,
  }[mode];
  const right = [];
  if (mode === 'edit') {
    const regen = button('重新生成种子', async () => {
      if (!confirm('重新生成种子后，这个环境的 Canvas、音频等指纹都会变，网站会把它当成一台新设备。确定？')) return;
      await api('POST', `/api/envs/${id}/regen-seed`); toast('已重新生成'); envForm(mode, id);
    });
    regen.disabled = locked;
    right.push(regen);
  }

  const hasP2 = !!(src && src.proxy.secondary && src.proxy.secondary.type);
  view().replaceChildren(
    pageTop(title, sub, ...right),
    ...(running ? [h('div.banner', { text: '环境运行中：代理和指纹只读，只能改名称和账号。关闭环境后才能修改其余字段。' })] : []),
    h('div.form', {},
      h('div', {},
        h('div.card', {}, sec('👤', '账号', '这个环境要登录的自家服务账号'),
          h('div.grid.g2', {},
            field('名称', name), field('备注', acNote),
            field('登录账号', acUser), field('登录密码', pwBox(acPass)))),
        h('div.card', {}, sec('⇄', '主代理', '浏览器只连本机中转；中转带认证连这条代理，失败即断，绝不直连'),
          proxyGrid(p1, true, field('指定出口 IP', exitIP)),
          h('div', { style: 'margin-top:12px' }, testBtn),
          checkOut,
          h('details', { style: 'margin-top:16px', open: hasP2 },
            h('summary', { style: 'cursor:pointer;color:var(--pri);font-size:13px', text: '＋ 备用代理（可选，只能在环境关闭时手动切换）' }),
            h('div', { style: 'margin-top:12px' }, proxyGrid(p2, false, field('当前使用', active))))),
        h('div.card', {}, sec('🌐', '时区与语言', '建议和出口国家一致'),
          h('div.grid', { style: 'grid-template-columns:1fr 1fr auto;align-items:end' },
            field('时区', tz), field('语言', lang), zoneBtn)),
        h('div.card', {}, sec('▦', '积木开关', '默认全开；关掉的项目会露出真机信息'), chips),
        h('div.actions', {}, button('取消', () => go('list')), save)),
      h('div.sticky', {}, h('div.card', {}, h('h3', { text: mode === 'edit' ? '当前配置' : '创建后的样子' }), kv,
        h('div.hint', { style: 'margin-top:14px', text: locked ? '环境运行中，代理和指纹不可改。' : '保存前可先点“测试代理”确认能连通。' })))));
}

// ---------- 详情 ----------
routes.detail = async id => {
  const [{ env, row }, log, reports, ts] = await Promise.all([
    api('GET', '/api/envs/' + id), api('GET', `/api/envs/${id}/log`), api('GET', `/api/envs/${id}/reports`), api('GET', '/api/templates')]);
  const tpl = ts.find(t => t.name === env.fingerprint.template);
  const up = env.proxy.primary;
  const again = () => routes.detail(id);
  const dash = v => (v === '' || v == null ? '—' : String(v));
  const kv = (...pairs) => h('dl.kv', {}, pairs.flatMap(([k, v]) => [h('dt', { text: k }), v instanceof Node ? h('dd', {}, v) : h('dd', { text: dash(v) })]));
  const pass = h('input', { type: 'password', value: env.account.password, readOnly: true });
  const ppass = h('input', { type: 'password', value: up.password, readOnly: true });
  const sec2 = env.proxy.secondary;
  const off = Object.entries(env.modules).filter(([, v]) => !v).map(([k]) => MODULE_NAMES[k] || k);
  const last = reports[0];
  const lastText = last ? `${last.total - last.failed} / ${last.total} 通过` : '还没有';
  const created = new Date(env.created_at).toLocaleString('zh-CN', { hour12: false });

  const ops = [];
  if (row.status === 'closed') ops.push(button('▶ 打开', openEnv(row, again), 'pri'));
  else ops.push(button(row.status === 'starting' ? '取消' : '关闭', closeEnv(row, again)));
  ops.push(button('检测', () => checkEnv(row)));
  if (row.status === 'closed') ops.push(button('编辑', () => go('edit/' + id)));
  ops.push(button('← 返回', () => go('list'), 'ghost'));

  const mini = (k, v, color) => h('div.mini', {}, h('div.k', { text: k }), h('div.v.mono', { style: color ? 'color:' + color : null, text: v }));
  view().replaceChildren(
    h('div.card', {},
      h('div.hero', {}, avatar(env.id, env.name),
        h('div', {}, h('h2', {}, env.name || env.id, pill(row.status)),
          h('div.mute', { text: `${env.id} · ${env.account.username || '—'} · 创建于 ${created}` })),
        h('span', { style: 'flex:1' }), ...ops),
      row.last_err ? h('div.result.bad', { text: row.last_err }) : null,
      h('div.dgrid', { style: 'margin-top:18px' },
        mini('出口 IP', `${dash(row.last_ip || env.proxy.exit_ip)} · ${env.proxy.exit_country || '国家未记录'}`),
        mini('本次中转端口', row.port ? '127.0.0.1:' + row.port : '未运行'),
        mini('最近检测', lastText, last ? (last.failed ? 'var(--bad)' : 'var(--ok)') : ''))),
    h('div.grid.g2', { style: 'align-items:start' },
      h('div.card', {}, h('h3', { text: '账号与网络' }), kv(
        ['登录账号', env.account.username],
        ['登录密码', pwBox(pass)],
        ['备注', env.account.note],
        ['主代理', h('span', {}, h('span.tag.blue', { text: up.type }), h('span.mono', { text: `${up.username}@${up.host}:${up.port}` }))],
        ['代理密码', pwBox(ppass)],
        ['备用代理', sec2 && sec2.type ? h('span', {}, h('span.tag.blue', { text: sec2.type }), h('span.mono', { text: `${sec2.username}@${sec2.host}:${sec2.port}` })) : ''],
        ['当前使用', env.proxy.active === 'secondary' ? '备用代理' : '主代理'],
        ['指定出口', h('span.mono', { text: dash(env.proxy.exit_ip) })])),
      h('div.card', {}, h('h3', { text: '指纹' }), kv(
        ['模板', h('span', {}, tpl ? tplLabel(tpl) : env.fingerprint.template,
          h('div.mute', { text: `系统 ${env.fingerprint.platform_version} · ${env.fingerprint.hardware_concurrency} 核` }))],
        ['种子', h('span.mono', { text: String(env.fingerprint.seed.fingerprint) })],
        ['时区 / 语言', `${zoneLabel(env.fingerprint.timezone)} / ${(env.fingerprint.languages || []).join(',')}`],
        ['窗口', `${env.fingerprint.seed.window.width} × ${env.fingerprint.seed.window.height}`],
        ['已关闭积木', off.length ? h('span', {}, off.map(t => h('span.tag', { text: t }))) : '无'],
        ['浏览器版本', env.chromium_version]))),
    reportCard(reports),
    h('div.card', {}, h('h3', {}, '运行日志 ', h('span.mute', { text: '含检测记录，最近 64KB' }), h('span.sp'), button('刷新', again, 'sm')),
      h('pre.log', { text: log || '还没有日志' })));
  const lg = view().querySelector('.log');
  lg.scrollTop = lg.scrollHeight;
};

// 检测报告历史（需求规格第 9 节：reports/ 下按时间保存，可在管理页面查看）。
// 只列摘要；有未通过或需要留意的项时，点“展开”看目标值与实际值。
function reportCard(rs) {
  const fmt = t => new Date(t).toLocaleString('zh-CN', { hour12: false });
  const items = rs.map(r => {
    const bad = r.bad.filter(b => !b.ok), warn = r.bad.filter(b => b.ok);
    const okN = r.total - r.failed;
    const ring = h('div.ring', { style: r.failed ? 'background:var(--badsoft);color:var(--bad)' : 'background:var(--oksoft);color:var(--ok)', text: String(okN) });
    const tags = [
      ...bad.slice(0, 2).map(b => h('span.tag.red', { text: b.item })),
      bad.length > 2 ? h('span.tag.red', { text: `等 ${bad.length} 项` }) : null,
      warn.length ? h('span.tag.yel', { text: `${warn.length} 项只提示` }) : null];
    const detail = h('table', { hidden: true }, h('tbody', {}, r.bad.map(b => h('tr', {},
      h('td', { text: (b.ok ? '△ ' : '✗ ') + b.item }),
      h('td.mute', { text: '目标 ' + (b.want || '—') }),
      h('td', { text: '实际 ' + (b.got || '—') }),
      h('td.mute', { text: b.where || '' })))));
    const tog = r.bad.length ? h('button.btn.sm', { type: 'button', text: '展开', onclick: e => {
      detail.hidden = !detail.hidden; e.target.textContent = detail.hidden ? '展开' : '收起';
    } }) : null;
    return h('div.rep', {},
      h('div.rh', {}, ring,
        h('div', { style: 'flex:1;min-width:0' }, h('b', { text: r.failed ? `${okN} 项通过，${r.failed} 项不一致` : `${r.total} 项全部通过` }),
          h('div.mute', { text: `${fmt(r.time)} · 种子 ${r.seed} · ${r.file}` })),
        tags, tog),
      detail);
  });
  return h('div.card', {},
    h('h3', {}, '检测报告历史 ', h('span.mute', { text: rs.length ? `${rs.length} 份，最新在前` : '' })),
    rs.length ? items
      : h('div.mute', { text: '还没有检测报告。测试模式下打开检测页，或运行 envbrowser fpcheck，报告会保存到环境目录的 reports/ 下。' }));
}
