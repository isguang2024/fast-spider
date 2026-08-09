package nodeui

const localUIHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="ui-token" content="{{UI_TOKEN}}">
  <title>Fast Spider Node</title>
  <style>
    :root{color-scheme:light;--bg:#f4f4f1;--panel:#fff;--line:#deded8;--text:#171717;--muted:#6d6d67;--soft:#f8f8f5;--ok:#1d7a45;--warn:#a45a00;--bad:#b42318;--accent:#171717}
    *{box-sizing:border-box} body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
    button,input,textarea{font:inherit}button,input{min-height:44px}button{cursor:pointer}button:focus-visible,input:focus-visible,textarea:focus-visible,summary:focus-visible{outline:2px solid #4f4f49;outline-offset:2px}.shell{width:min(1100px,calc(100% - 28px));margin:24px auto 50px}.top{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.brand{display:flex;align-items:center;gap:12px}.logo{width:38px;height:38px;border-radius:11px;background:#171717;color:#fff;display:grid;place-items:center;font-weight:800}.brand h1{font-size:18px;margin:0}.brand small{display:block;color:var(--muted);margin-top:1px}.top-actions{display:flex;align-items:center;gap:8px}.status{display:flex;align-items:center;gap:8px;border:1px solid var(--line);background:var(--panel);padding:8px 12px;border-radius:999px}.dot{width:8px;height:8px;border-radius:50%;background:#999}.dot.ok{background:var(--ok)}.dot.warn{background:var(--warn)}.dot.bad{background:var(--bad)}
    .layout{display:grid;grid-template-columns:190px minmax(0,1fr);gap:16px}.nav,.panel{background:var(--panel);border:1px solid var(--line);border-radius:16px}.nav{padding:10px;height:max-content;position:sticky;top:18px}.nav button{width:100%;border:0;background:transparent;text-align:left;padding:10px 12px;border-radius:10px;color:var(--muted);margin:2px 0}.nav button.active{background:#171717;color:#fff}.content{min-width:0}.panel{padding:22px;margin-bottom:16px}.panel h2{font-size:18px;margin:0 0 5px}.copy{color:var(--muted);margin:0 0 18px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.field{display:grid;gap:6px}.field.full{grid-column:1/-1}.field span{font-weight:650}.field input,.field textarea{width:100%;border:1px solid var(--line);border-radius:10px;padding:10px 11px;background:#fff;outline:none}.field textarea{min-height:150px;resize:vertical;line-height:1.55}.field input:focus,.field textarea:focus{border-color:#777}.secret-row{display:grid;grid-template-columns:1fr auto;gap:8px}.secondary,.primary,.danger{border-radius:10px;padding:9px 13px;border:1px solid var(--line);background:#fff}.primary{background:#171717;border-color:#171717;color:#fff}.danger{color:var(--bad)}.actions{display:flex;align-items:center;gap:10px;margin-top:16px;flex-wrap:wrap}.hint{color:var(--muted);font-size:12px}.message{display:none;margin:0 0 16px;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--soft)}.message.show{display:block}.message.error{border-color:#f0b7b2;color:var(--bad);background:#fff7f6}
    .facts{display:grid;grid-template-columns:repeat(3,1fr);gap:10px;margin-top:18px}.fact{border:1px solid var(--line);background:var(--soft);border-radius:12px;padding:12px}.fact span{display:block;color:var(--muted);font-size:12px}.fact strong{display:block;margin-top:3px;word-break:break-all}.section{display:none}.section.active{display:block}.switch{display:flex;align-items:center;gap:9px;border:1px solid var(--line);border-radius:10px;padding:10px 11px;background:var(--soft)}.switch input{width:18px;height:18px}.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.advanced{margin-top:14px}.advanced summary{cursor:pointer;color:var(--muted)}
    @media(max-width:760px){body{font-size:16px}.layout{grid-template-columns:1fr}.nav{position:static;display:flex;gap:6px}.nav button{text-align:center}.grid,.facts{grid-template-columns:1fr}.top{align-items:flex-start;flex-direction:column}.top-actions{width:100%;flex-wrap:wrap}.status{flex:1;justify-content:center}}
  </style>
</head>
<body>
  <div class="shell">
    <header class="top">
      <div class="brand"><div class="logo">FS</div><div><h1>Fast Spider Node</h1><small>本机客户端 · {{VERSION}}</small></div></div>
      <div class="top-actions"><div class="status"><span id="status-dot" class="dot"></span><strong id="status-text">读取状态…</strong></div><button id="exit-app" class="secondary" type="button">退出客户端</button></div>
    </header>
    <div id="message" class="message" role="status"></div>
    <div class="layout">
      <nav class="nav" aria-label="本地设置">
        <button class="active" data-tab="connect">连接</button>
        <button data-tab="config">本地配置</button>
      </nav>
      <main class="content">
        <section id="tab-connect" class="section active">
          <div class="panel">
            <h2>连接这台电脑</h2>
            <p class="copy">连接密钥只用来确认这台设备归属哪个后台用户。一个有效密钥可以给同一账户下多台客户端重复使用；客户端不会保存密钥。Windows 下关闭这个页面不会退出 Node，客户端会继续驻留系统托盘。</p>
            <form id="connect-form">
              <div class="grid">
                <label class="field full"><span>Hub 地址</span><input id="connect-hub" type="url" maxlength="2048" placeholder="https://sharedservices.example.com/fast-spider" required></label>
                <label class="field full"><span>连接密钥</span><div class="secret-row"><input id="connect-token" type="password" maxlength="256" autocomplete="off" placeholder="ctk_..." required><button id="toggle-token" class="secondary" type="button">显示</button></div></label>
                <label class="field full"><span>客户端名称</span><input id="connect-name" maxlength="128" required></label>
              </div>
              <div class="actions"><button class="primary" type="submit">连接</button><span class="hint">成功后改用本机设备密钥持续连接，不再需要连接密钥。</span></div>
            </form>
            <div class="facts">
              <div class="fact"><span>登记模式</span><strong id="fact-registration">connection_token</strong></div>
              <div class="fact"><span>运行凭据</span><strong id="fact-runtime">device_key</strong></div>
              <div class="fact"><span>配置范围</span><strong id="fact-scope">local_node</strong></div>
            </div>
          </div>
          <div class="panel">
            <h2>当前设备</h2>
            <div class="facts">
              <div class="fact"><span>Machine ID</span><strong id="machine-id">未登记</strong></div>
              <div class="fact"><span>Hub</span><strong id="machine-hub">—</strong></div>
              <div class="fact"><span>Token 是否保存</span><strong>否</strong></div>
              <div class="fact"><span>文件权限</span><strong>当前系统用户 · 全电脑</strong></div>
              <div class="fact"><span>托盘状态</span><strong id="tray-state">读取中…</strong></div>
            </div>
          </div>
        </section>

        <section id="tab-config" class="section">
          <div class="panel">
            <h2>本地配置</h2>
            <p class="copy">这些设置只保存在这台电脑，不同步到 Hub。Fast Spider 直接以当前系统用户身份操作本机文件、Shell、Git 和本地能力，不需要额外登记目录。</p>
            <form id="config-form">
              <div class="grid">
                <label class="field full"><span>默认 Hub 地址（连接/重新登记时使用）</span><input id="config-hub" type="url" maxlength="2048"></label>
                <label class="field"><span>客户端名称</span><input id="config-name" maxlength="128" required><small class="hint">保存后会在下一次连接时同步到后台；管理员备注与这里完全独立。</small></label>
                <label class="field"><span>浏览器 Sidecar 目录</span><input id="config-browser" maxlength="4096" placeholder="留空使用默认目录"></label>
                <label class="switch"><input id="config-bridge" type="checkbox"><span><strong>Local Bridge</strong><br><small class="hint">允许当前系统用户的本地 AI 客户端调用 Node。</small></span></label>
                <label class="switch"><input id="config-autostart" type="checkbox"><span><strong>登录 Windows 后自动启动</strong><br><small class="hint">登录后隐藏启动到系统托盘，不弹出配置页面；仍然是同一个 EXE。</small></span></label>
                <label class="switch"><input id="config-autoupdate" type="checkbox"><span><strong>自动更新</strong><br><small class="hint">后台检查并下载新版本；下次启动时自动完成替换。</small></span></label>
              </div>
              <details class="advanced"><summary>开发环境选项</summary><label class="switch" style="margin-top:10px"><input id="config-insecure" type="checkbox"><span><strong>允许本机开发 HTTP Hub</strong><br><small class="hint">正式环境保持关闭，只使用 HTTPS。</small></span></label></details>
              <div class="actions"><button class="primary" type="submit">保存本地配置</button><span id="data-dir" class="hint mono"></span></div>
            </form>
          </div>
          <div class="panel">
            <h2>版本与扩展组件</h2>
            <p class="copy">主程序保持单文件 EXE；Browser 等大型能力以后按需下载到组件目录。更新包由当前 Hub 签名，并同时校验 SHA256。</p>
            <div class="facts">
              <div class="fact"><span>当前版本</span><strong id="update-current">{{VERSION}}</strong></div>
              <div class="fact"><span>最新版本</span><strong id="update-latest">尚未检查</strong></div>
              <div class="fact"><span>更新状态</span><strong id="update-state">尚未检查</strong></div>
            </div>
            <div class="actions"><button id="update-check" class="secondary" type="button">检查更新</button><button id="update-install" class="primary" type="button" disabled>立即升级</button><span id="update-time" class="hint"></span></div>
            <p class="hint mono" id="component-root"></p>
          </div>
        </section>

      </main>
    </div>
  </div>
<script>
(() => {
  const token = document.querySelector('meta[name="ui-token"]').content;
  const $ = (id) => document.getElementById(id);
  let current = null;
  let busy = false;
  let configDirty = false;

  async function api(path, options = {}) {
    const headers = Object.assign({'X-Fast-Spider-UI-Token': token}, options.headers || {});
    if (options.body) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, Object.assign({}, options, {headers}));
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || ('HTTP ' + response.status));
    return data;
  }

  function message(text, error = false) {
    const box = $('message');
    box.textContent = text || '';
    box.className = 'message' + (text ? ' show' : '') + (error ? ' error' : '');
  }

  function runtimeLabel(value) {
    return ({online:'已连接',connecting:'正在连接',reconnecting:'正在重连',starting:'正在启动',stopped:'已停止',not_registered:'等待连接',external_running:'旧版后台实例运行中',error:'连接异常'})[value] || value || '未知';
  }

  function renderStatus(status) {
    current = status;
    const dot = $('status-dot');
    dot.className = 'dot ' + (status.runtimeStatus === 'online' ? 'ok' : status.runtimeStatus === 'error' ? 'bad' : 'warn');
    $('status-text').textContent = runtimeLabel(status.runtimeStatus);
    $('fact-registration').textContent = status.registrationMode;
    $('fact-runtime').textContent = status.runtimeCredential;
    $('fact-scope').textContent = status.configurationScope;
    $('machine-id').textContent = status.machineId || '未登记';
    $('machine-hub').textContent = status.hubUrl || '—';
    $('tray-state').textContent = status.traySupported ? (status.trayActive ? '已驻留 · 右键可退出' : '未启动') : '当前系统不支持';
    $('data-dir').textContent = '配置目录：' + status.dataDir;
    $('exit-app').textContent = status.runtimeOwned ? '退出客户端' : '关闭界面';
    if (status.runtimeStatus === 'external_running' && status.runtimeError) message(status.runtimeError);

    const cfg = status.config || {};
    if (!document.activeElement || !['connect-hub','connect-name'].includes(document.activeElement.id)) {
      $('connect-hub').value = cfg.hubUrl || status.hubUrl || '';
      $('connect-name').value = cfg.machineName || '';
    }
    if (!configDirty) {
      $('config-hub').value = cfg.hubUrl || status.hubUrl || '';
      $('config-name').value = cfg.machineName || '';
      $('config-browser').value = cfg.browserSidecarDir || '';
      $('config-bridge').checked = !!cfg.localBridgeEnabled;
      $('config-autostart').checked = !!status.autoStartEnabled;
      $('config-autoupdate').checked = !!cfg.autoUpdateEnabled;
      $('config-insecure').checked = !!cfg.allowInsecureLocalHub;
    }
    $('config-autostart').disabled = !status.autoStartSupported;
    $('component-root').textContent = '组件目录：' + (status.componentRoot || '—');
    renderUpdate(status.update || {});
  }

  function renderUpdate(update) {
    $('update-current').textContent = update.currentVersion || '{{VERSION}}';
    $('update-latest').textContent = update.latestVersion || '尚未检查';
    let state = '尚未检查';
    if (update.checking) state = '正在检查';
    else if (update.error) state = '检查失败';
    else if (update.ready) state = '已下载，等待安装';
    else if (update.available) state = '发现新版本';
    else if (update.latestVersion) state = '已是最新版本';
    $('update-state').textContent = state;
    $('update-time').textContent = update.error ? update.error : (update.lastCheckedAt ? '上次检查：' + update.lastCheckedAt : '');
    $('update-install').disabled = !update.available || update.checking;
    $('update-check').disabled = !!update.checking;
  }

  async function refresh() {
    if (busy) return;
    try { renderStatus(await api('/api/status')); } catch (e) { message(e.message, true); }
  }

  document.querySelectorAll('.nav button').forEach(button => button.addEventListener('click', () => {
    document.querySelectorAll('.nav button').forEach(x => x.classList.toggle('active', x === button));
    document.querySelectorAll('.section').forEach(x => x.classList.toggle('active', x.id === 'tab-' + button.dataset.tab));
  }));

  $('toggle-token').addEventListener('click', () => {
    const input = $('connect-token'); input.type = input.type === 'password' ? 'text' : 'password'; $('toggle-token').textContent = input.type === 'password' ? '显示' : '隐藏';
  });

  $('connect-form').addEventListener('submit', async event => {
    event.preventDefault(); if (busy) return; busy = true; const submit = event.currentTarget.querySelector('button[type="submit"]'); submit.disabled = true; message('正在登记并连接这台设备…');
    try {
      const data = await api('/api/connect',{method:'POST',body:JSON.stringify({hubUrl:$('connect-hub').value,token:$('connect-token').value,machineName:$('connect-name').value})});
      $('connect-token').value=''; renderStatus(data); message('设备已登记。连接密钥没有保存在本机，后续使用设备密钥自动连接。');
    } catch (e) { message(e.message,true); } finally { busy=false; submit.disabled=false; }
  });

  $('config-form').addEventListener('input', () => { configDirty = true; });
  $('config-form').addEventListener('change', () => { configDirty = true; });

  $('config-form').addEventListener('submit', async event => {
    event.preventDefault(); if (busy) return; busy=true; const submit = event.currentTarget.querySelector('button[type="submit"]'); submit.disabled=true;
    try {
      const data = await api('/api/config',{method:'POST',body:JSON.stringify({hubUrl:$('config-hub').value,machineName:$('config-name').value,browserSidecarDir:$('config-browser').value,localBridgeEnabled:$('config-bridge').checked,autoStartEnabled:$('config-autostart').checked,autoUpdateEnabled:$('config-autoupdate').checked,allowInsecureLocalHub:$('config-insecure').checked})});
      configDirty = false; renderStatus(data); message('本地配置已保存。');
    } catch (e) { message(e.message,true); } finally { busy=false; submit.disabled=false; }
  });

  $('update-check').addEventListener('click', async () => {
    if (busy) return; busy=true; message('正在检查新版本…');
    try { const data=await api('/api/update/check',{method:'POST',body:'{}'}); renderStatus(data); message(data.update && data.update.available ? '发现新版本，可以立即升级。' : '当前已经是最新版本。'); }
    catch(e){message(e.message,true);} finally{busy=false;}
  });

  $('update-install').addEventListener('click', async () => {
    if (busy || !confirm('下载并安装最新版本？客户端会自动退出、替换当前 EXE，然后重新打开。')) return;
    busy=true; message('正在下载并校验新版本…');
    try { const data=await api('/api/update/install',{method:'POST',body:'{}'}); if(data.restarting){document.body.textContent='更新已校验完成，正在替换 Fast Spider Node 并重新启动…';} else {renderStatus(data.status); message('当前已经是最新版本。');} }
    catch(e){message(e.message,true);busy=false;}
  });

  $('exit-app').addEventListener('click', async () => {
    const ownsRuntime = current && current.runtimeOwned;
    const prompt = ownsRuntime ? '真正退出 Fast Spider Node？关闭浏览器页面不会退出；这里确认后会结束托盘和设备连接，MCP 将无法访问这台设备。' : '关闭本地界面？旧的无界面 Node 进程会继续运行。';
    if (!confirm(prompt)) return;
    try { await api('/api/exit',{method:'POST',body:'{}'}); document.body.textContent=ownsRuntime ? 'Fast Spider Node 已退出，可以关闭此窗口。' : '本地界面已关闭；旧的无界面 Node 仍在运行。'; } catch(e) { message(e.message,true); }
  });

  refresh(); setInterval(refresh, 2000);
})();
</script>
</body>
</html>`
