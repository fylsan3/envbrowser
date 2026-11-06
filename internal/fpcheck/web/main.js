// 检测页主脚本：在主页面、iframe、Worker、Service Worker 分别采集，
// 加上只在主页面能测的项（屏幕、音频、字体、ClientRects、WebRTC、本机端口），
// 一起发回本机检测服务，由服务端与环境配置比对并保存报告。
"use strict";

const qs = new URLSearchParams(location.search);
const ENV = qs.get("env") || "";
const out = document.getElementById("out");
function say(s) {
  out.textContent += s + "\n";
}

function withTimeout(p, ms, what) {
  return Promise.race([p, new Promise((_, rej) => setTimeout(() => rej(new Error(what + " 超时")), ms))]);
}

async function audioHash() {
  const a = new OfflineAudioContext(1, 5000, 44100);
  const o = a.createOscillator();
  o.type = "triangle";
  o.frequency.value = 10000;
  const cp = a.createDynamicsCompressor();
  o.connect(cp);
  cp.connect(a.destination);
  o.start(0);
  const b = await a.startRendering();
  return fpHash(b.getChannelData(0).slice(4000).buffer);
}

const FONTS = ["Arial", "Helvetica Neue", "Menlo", "PingFang SC", "Hiragino Sans", "Songti SC", "Avenir", "Futura",
  "Gill Sans", "Monaco", "Courier New", "Times New Roman", "Verdana", "Tahoma", "Segoe UI", "Calibri",
  "Microsoft YaHei", "SimSun", "Ubuntu", "DejaVu Sans"];

// 用宽度判断字体是否存在（与默认字体比较），同时记录各字体的测宽（ClientRects 扰动）
function fontProbe() {
  const box = document.createElement("div");
  box.style.cssText = "position:absolute;left:-9999px;top:0;font-size:40px";
  document.body.appendChild(box);
  const text = "mmmmmmmmmmlli 指纹 WwQq";
  const base = {};
  const bases = ["monospace", "serif", "sans-serif"];
  for (const b of bases) {
    const s = document.createElement("span");
    s.style.fontFamily = b;
    s.textContent = text;
    box.appendChild(s);
    base[b] = s.getBoundingClientRect().width;
  }
  const have = [];
  const widths = [];
  for (const f of FONTS) {
    let found = false;
    for (const b of bases) {
      const s = document.createElement("span");
      s.style.fontFamily = '"' + f + '",' + b;
      s.textContent = text;
      box.appendChild(s);
      const w = s.getBoundingClientRect().width;
      widths.push(w.toFixed(6));
      if (w !== base[b]) found = true;
    }
    if (found) have.push(f);
  }
  box.remove();
  return { fonts: have.join(","), rects: widths.join("|") };
}

// WebRTC：收集 ICE 候选地址（借助公共 STUN）。正常应看不到本机内网 IP 和真实公网 IP。
async function webrtc() {
  const ips = new Set();
  const types = new Set();
  const pc = new RTCPeerConnection({ iceServers: [{ urls: "stun:stun.l.google.com:19302" }] });
  pc.createDataChannel("x");
  const done = new Promise((res) => {
    pc.onicecandidate = (e) => {
      if (!e.candidate) return res();
      const p = e.candidate.candidate.split(" ");
      if (p.length > 7) {
        ips.add(p[4]);
        types.add(p[7]);
      }
    };
    setTimeout(res, 4000);
  });
  await pc.setLocalDescription(await pc.createOffer());
  await done;
  pc.close();
  return { rtcIPs: Array.from(ips).join(","), rtcTypes: Array.from(types).join(",") };
}

// 本机端口扫描：网页脚本尝试连本机若干端口。中转对它们一律拒绝，结果应完全一样。
async function portScan() {
  const ports = [22, 80, 443, 3000, 5000, 8080, 8800, 9001, 9222, 20001, 20002, 20999];
  const res = [];
  for (const p of ports) {
    const t0 = performance.now();
    let r;
    try {
      await withTimeout(fetch("http://127.0.0.1:" + p + "/", { mode: "no-cors", cache: "no-store" }), 5000, "端口");
      r = "可连";
    } catch (e) {
      r = e.message.indexOf("超时") >= 0 ? "超时" : "拒绝";
    }
    res.push({ port: p, result: r, ms: Math.round(performance.now() - t0) });
  }
  return res;
}

function fromIframe() {
  return new Promise((res, rej) => {
    const f = document.createElement("iframe");
    f.src = "/frame.html";
    f.style.display = "none";
    const h = (e) => {
      if (e.source === f.contentWindow) {
        removeEventListener("message", h);
        f.remove();
        res(e.data);
      }
    };
    addEventListener("message", h);
    f.onerror = () => rej(new Error("iframe 加载失败"));
    document.body.appendChild(f);
  });
}

function fromWorker() {
  return new Promise((res, rej) => {
    const w = new Worker("/worker.js");
    w.onmessage = (e) => {
      w.terminate();
      res(e.data);
    };
    w.onerror = (e) => rej(new Error("Worker 出错 " + e.message));
  });
}

async function fromServiceWorker() {
  const reg = await navigator.serviceWorker.register("/sw.js");
  const sw = reg.installing || reg.waiting || reg.active;
  if (sw.state !== "activated") {
    await new Promise((res) => sw.addEventListener("statechange", () => sw.state === "activated" && res()));
  }
  const ch = new MessageChannel();
  const p = new Promise((res) => (ch.port1.onmessage = (e) => res(e.data)));
  sw.postMessage("collect", [ch.port2]);
  const r = await p;
  await reg.unregister();
  return r;
}

async function run() {
  const rep = { env: ENV, places: [], extra: {}, errors: [] };
  const step = async (name, fn) => {
    try {
      return await withTimeout(fn(), 15000, name);
    } catch (e) {
      rep.errors.push(name + ": " + e.message);
      say("× " + name + "：" + e.message);
      return null;
    }
  };
  say("采集主页面…");
  const main = await step("主页面", () => collectFP("主页面"));
  if (main) rep.places.push(main);
  for (const [name, fn] of [["iframe", fromIframe], ["Worker", fromWorker], ["Service Worker", fromServiceWorker]]) {
    say("采集 " + name + "…");
    const r = await step(name, fn);
    if (r) rep.places.push(r);
  }
  const ex = rep.extra;
  // Cookie 隔离：先读已有 Cookie，再写入本环境标记。别的环境写的标记不应出现在这里。
  ex.cookie = document.cookie;
  document.cookie = "fp_env=" + ENV + "; path=/; max-age=86400; samesite=strict";
  ex.screen = [screen.width, screen.height, screen.colorDepth, devicePixelRatio].join("x");
  ex.window = [outerWidth, outerHeight].join("x");
  ex.audio = await step("音频", audioHash);
  Object.assign(ex, (await step("字体", async () => fontProbe())) || {});
  say("WebRTC…");
  Object.assign(ex, (await step("WebRTC", webrtc)) || {});
  say("本机端口扫描…");
  ex.ports = await step("端口扫描", portScan);
  say("提交报告…");
  const r = await fetch("/report", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(rep),
  });
  const t = await r.json();
  render(t);
}

function render(t) {
  out.textContent = "";
  document.getElementById("sum").textContent = t.summary || "";
  const tb = document.getElementById("rows");
  for (const row of t.rows || []) {
    const tr = document.createElement("tr");
    tr.className = row.ok ? "ok" : row.warn ? "warn" : "bad";
    for (const v of [row.item, row.want, row.got, row.where, row.ok ? "一致" : row.warn ? "注意" : "不一致"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.appendChild(td);
    }
    tb.appendChild(tr);
  }
  document.title = "FPDONE " + (t.file || "");
}

run().catch((e) => say("出错：" + e));
