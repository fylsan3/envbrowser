// 采集指纹。主页面、iframe、Worker、Service Worker 共用这一份代码。
// 只读取，不修改任何东西。结果只发回本机检测页。
"use strict";

async function fpHash(buf) {
  const d = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(d).slice(0, 8), (x) => x.toString(16).padStart(2, "0")).join("");
}

function fpCanvas(w, h) {
  if (typeof document !== "undefined") {
    const c = document.createElement("canvas");
    c.width = w;
    c.height = h;
    return c;
  }
  return new OffscreenCanvas(w, h);
}

// 同样的绘制，在主页面、iframe、Worker 中读回像素算哈希
async function fpCanvasHash() {
  const c = fpCanvas(240, 60);
  const x = c.getContext("2d");
  x.textBaseline = "top";
  x.font = "16px Arial";
  x.fillStyle = "#f60";
  x.fillRect(10, 10, 100, 30);
  x.fillStyle = "#069";
  x.fillText("envbrowser 指纹 123", 4, 20);
  x.strokeStyle = "rgba(102,204,0,0.7)";
  x.beginPath();
  x.arc(180, 30, 20, 0, Math.PI * 2);
  x.stroke();
  return fpHash(x.getImageData(0, 0, 240, 60).data);
}

async function fpWebGL() {
  const r = {};
  const c = fpCanvas(64, 64);
  const g = c.getContext("webgl");
  if (!g) return { glErr: "无 WebGL" };
  const d = g.getExtension("WEBGL_debug_renderer_info");
  r.glVendor = d ? g.getParameter(d.UNMASKED_VENDOR_WEBGL) : "";
  r.glRenderer = d ? g.getParameter(d.UNMASKED_RENDERER_WEBGL) : "";
  // 画一个渐变三角形，读回像素算哈希（WebGL 图像扰动）
  const vs = g.createShader(g.VERTEX_SHADER);
  g.shaderSource(vs, "attribute vec2 p;varying vec2 v;void main(){v=p;gl_Position=vec4(p,0,1);}");
  g.compileShader(vs);
  const fs = g.createShader(g.FRAGMENT_SHADER);
  g.shaderSource(fs, "precision mediump float;varying vec2 v;void main(){gl_FragColor=vec4(v*0.5+0.5,0.3,1);}");
  g.compileShader(fs);
  const pr = g.createProgram();
  g.attachShader(pr, vs);
  g.attachShader(pr, fs);
  g.linkProgram(pr);
  g.useProgram(pr);
  const b = g.createBuffer();
  g.bindBuffer(g.ARRAY_BUFFER, b);
  g.bufferData(g.ARRAY_BUFFER, new Float32Array([-0.9, -0.9, 0.9, -0.7, 0, 0.9]), g.STATIC_DRAW);
  const loc = g.getAttribLocation(pr, "p");
  g.enableVertexAttribArray(loc);
  g.vertexAttribPointer(loc, 2, g.FLOAT, false, 0, 0);
  g.clearColor(0.1, 0.2, 0.3, 1);
  g.clear(g.COLOR_BUFFER_BIT);
  g.drawArrays(g.TRIANGLES, 0, 3);
  const px = new Uint8Array(64 * 64 * 4);
  g.readPixels(0, 0, 64, 64, g.RGBA, g.UNSIGNED_BYTE, px);
  r.glImage = await fpHash(px);
  return r;
}

async function collectFP(where) {
  const n = self.navigator;
  const r = { where: where };
  r.ua = n.userAgent;
  r.platform = n.platform;
  r.cores = n.hardwareConcurrency;
  r.mem = n.deviceMemory;
  r.lang = n.language;
  r.langs = (n.languages || []).join(",");
  r.tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  r.tzOffset = new Date(2026, 0, 15).getTimezoneOffset();
  r.webdriver = n.webdriver;
  if (n.userAgentData) {
    r.brands = n.userAgentData.brands.map((b) => b.brand + "/" + b.version).join(",");
    r.uaPlatform = n.userAgentData.platform;
    try {
      const h = await n.userAgentData.getHighEntropyValues(["platformVersion", "architecture", "fullVersionList", "model"]);
      r.platformVersion = h.platformVersion;
      r.arch = h.architecture;
      r.fullVersions = (h.fullVersionList || []).map((b) => b.brand + "/" + b.version).join(",");
    } catch (e) {
      r.uaErr = String(e);
    }
  }
  try {
    r.canvas = await fpCanvasHash();
  } catch (e) {
    r.canvas = "错误 " + e;
  }
  try {
    Object.assign(r, await fpWebGL());
  } catch (e) {
    r.glErr = String(e);
  }
  return r;
}
