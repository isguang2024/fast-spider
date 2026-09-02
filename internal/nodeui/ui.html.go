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
    button,input,textarea,select{font:inherit}button,input,select{min-height:44px}button{cursor:pointer}button:focus-visible,input:focus-visible,textarea:focus-visible,select:focus-visible,summary:focus-visible{outline:2px solid #4f4f49;outline-offset:2px}.shell{width:min(1100px,calc(100% - 28px));margin:24px auto 50px}.top{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.brand{display:flex;align-items:center;gap:12px}.logo{width:38px;height:38px;border-radius:11px;background:#171717;color:#fff;display:grid;place-items:center;font-weight:800}.brand h1{font-size:18px;margin:0}.brand small{display:block;color:var(--muted);margin-top:1px}.top-actions{display:flex;align-items:center;gap:8px}.status{display:flex;align-items:center;gap:8px;border:1px solid var(--line);background:var(--panel);padding:8px 12px;border-radius:999px}.dot{width:8px;height:8px;border-radius:50%;background:#999}.dot.ok{background:var(--ok)}.dot.warn{background:var(--warn)}.dot.bad{background:var(--bad)}
    .layout{display:grid;grid-template-columns:190px minmax(0,1fr);gap:16px}.nav,.panel{background:var(--panel);border:1px solid var(--line);border-radius:16px}.nav{padding:10px;height:max-content;position:sticky;top:18px}.nav button{width:100%;border:0;background:transparent;text-align:left;padding:10px 12px;border-radius:10px;color:var(--muted);margin:2px 0}.nav button.active{background:#171717;color:#fff}.content{min-width:0}.panel{padding:22px;margin-bottom:16px}.panel h2{font-size:18px;margin:0 0 5px}.copy{color:var(--muted);margin:0 0 18px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.field{display:grid;gap:6px}.field.full{grid-column:1/-1}.field span{font-weight:650}.field input,.field textarea{width:100%;border:1px solid var(--line);border-radius:10px;padding:10px 11px;background:#fff;outline:none}.field textarea{min-height:150px;resize:vertical;line-height:1.55}.field input:focus,.field textarea:focus{border-color:#777}.secret-row{display:grid;grid-template-columns:1fr auto;gap:8px}.secondary,.primary,.danger{border-radius:10px;padding:9px 13px;border:1px solid var(--line);background:#fff}.primary{background:#171717;border-color:#171717;color:#fff}.danger{color:var(--bad)}.actions{display:flex;align-items:center;gap:10px;margin-top:16px;flex-wrap:wrap}.hint{color:var(--muted);font-size:12px}.message{display:none;margin:0 0 16px;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--soft)}.message.show{display:block}.message.error{border-color:#f0b7b2;color:var(--bad);background:#fff7f6}
    .facts{display:grid;grid-template-columns:repeat(3,1fr);gap:10px;margin-top:18px}.fact{border:1px solid var(--line);background:var(--soft);border-radius:12px;padding:12px}.fact span{display:block;color:var(--muted);font-size:12px}.fact strong{display:block;margin-top:3px;word-break:break-word}.section{display:none}.section.active{display:block}.switch{display:flex;align-items:center;gap:9px;border:1px solid var(--line);border-radius:10px;padding:10px 11px;background:var(--soft)}.switch input{width:18px;height:18px}.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.advanced{margin-top:14px}.advanced summary{cursor:pointer;color:var(--muted)}[hidden]{display:none!important}
    .field select{width:100%;border:1px solid var(--line);border-radius:10px;padding:9px 11px;background:#fff}.progress-track{height:8px;border-radius:999px;background:#e6e6e0;overflow:hidden;margin-top:9px}.progress-fill{height:100%;background:var(--ok);width:0}.split{display:grid;grid-template-columns:1fr 1fr;gap:12px}.task-list{display:grid;gap:8px}.task-row{border:1px solid var(--line);border-radius:10px;padding:10px 11px;background:var(--soft)}.task-row strong{display:block}.task-row small{color:var(--muted)}.empty{color:var(--muted);padding:8px 0}.workspace{display:grid;grid-template-columns:230px minmax(0,1fr);gap:12px}.file-list{display:grid;gap:6px;align-content:start}.file-list button{text-align:left;min-height:38px;padding:7px 9px}.markdown-view{min-height:250px;max-height:520px;overflow:auto;margin:0;padding:14px;border:1px solid var(--line);border-radius:10px;background:#20201e;color:#f5f5f0;white-space:pre-wrap;word-break:break-word}.subhead{font-size:14px;margin:0 0 10px}
		.data-list{display:grid;gap:7px;margin-top:12px}.data-row{display:flex;justify-content:space-between;gap:14px;flex-wrap:wrap;border-bottom:1px solid var(--line);padding:7px 0}.data-row:last-child{border-bottom:0}.data-row span{color:var(--muted)}.data-row strong{text-align:right;word-break:break-word}.provider-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.provider-grid .panel{margin-bottom:0}.tag-list{display:flex;gap:6px;flex-wrap:wrap;margin-top:10px}.tag{border:1px solid var(--line);border-radius:999px;background:var(--soft);padding:4px 8px;font-size:12px}.notice{border:1px solid var(--line);border-radius:12px;background:var(--soft);padding:12px 14px;color:var(--muted)}.advanced-model-list{display:grid;gap:12px;margin-top:16px}.advanced-model-row{border:1px solid var(--line);border-radius:12px;background:var(--soft);padding:14px}.advanced-model-fields{display:grid;grid-template-columns:minmax(0,1.2fr) minmax(0,1fr) auto;gap:10px;align-items:end}.thinking-list{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}.thinking-list .switch{background:#fff;padding:7px 9px}.thinking-list .switch input{width:16px;height:16px;min-height:0}
    @media(max-width:760px){body{font-size:16px}.layout{grid-template-columns:1fr}.nav{position:static;display:flex;gap:6px;overflow:auto}.nav button{text-align:center;white-space:nowrap}.grid,.facts,.split,.workspace,.provider-grid{grid-template-columns:1fr}.top{align-items:flex-start;flex-direction:column}.top-actions{width:100%;flex-wrap:wrap}.status{flex:1;justify-content:center}}
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
        <button class="active" data-tab="connect">设备</button>
        <button data-tab="working">任务与进度</button>
		<button data-tab="ai">AI 与路由</button>
		<button data-tab="diagnostics">诊断</button>
		<button data-tab="components">组件</button>
		<button data-tab="operation-logs">操作日志</button>
        <button data-tab="config">本地配置</button>
      </nav>
      <main class="content">
        <section id="tab-connect" class="section active">
		  <div id="codex-session-mode-panel" class="panel" hidden>
			<h2>选择 Codex 会话模式</h2>
			<p class="copy">这项设置只保存在本机。共享模式不会让 Fast Spider 认领 Codex Desktop 会话；FS 接管模式会启用 Desktop owner/control 联动，已由 FS 加载的会话可能需要在 FS 中继续操作。</p>
			<form id="codex-session-mode-form">
			  <div class="grid">
				<label class="switch"><input name="startup-codex-session-mode" type="radio" value="shared" checked><span><strong>共享模式（推荐）</strong><br><small class="hint">FS 创建本地会话，但不通过 Desktop IPC 认领它们，Codex Desktop 不会因 FS 锁定会话。</small></span></label>
				<label class="switch"><input name="startup-codex-session-mode" type="radio" value="managed"><span><strong>FS 接管模式</strong><br><small class="hint">启用 Desktop owner/control 联动，适合需要由 FS 继续处理中断、审批等操作的场景。</small></span></label>
			  </div>
			  <div class="actions"><button class="primary" type="submit">保存并继续</button></div>
			</form>
		  </div>
          <div id="registration-panel" class="panel">
            <h2>首次连接</h2>
            <p class="copy">只需要登记一次。填写后台生成的连接密钥和客户端名称，成功后这台电脑会自动使用自己的设备密钥连接，后续启动不再要求输入连接密钥。</p>
            <form id="connect-form">
              <div class="grid">
                <label class="field full"><span>连接密钥</span><div class="secret-row"><input id="connect-token" type="password" maxlength="256" autocomplete="off" placeholder="ctk_..." required><button id="toggle-token" class="secondary" type="button">显示</button></div></label>
                <label class="field full"><span>客户端名称</span><input id="connect-name" maxlength="128" required></label>
              </div>
              <details class="advanced" open><summary>Hub 连接设置</summary><label class="field full" style="margin-top:10px"><span>Hub 地址</span><input id="connect-hub" type="url" maxlength="2048" placeholder="https://your-hub.example/fast-spider" required><small class="hint">填写你自己部署的 Fast Spider Hub HTTPS 地址；连接成功后保存在本机。</small></label></details>
              <div class="actions"><button class="primary" type="submit">连接这台电脑</button></div>
            </form>
          </div>
          <div id="registered-panel" class="panel" hidden>
            <h2>这台电脑已连接</h2>
            <p class="copy">设备已经完成登记。以后打开 Fast Spider 会自动连接 Hub，不需要再次输入连接密钥。</p>
            <div class="facts">
              <div class="fact"><span>客户端名称</span><strong id="machine-name">—</strong></div>
              <div class="fact"><span>Hub</span><strong id="machine-hub">—</strong></div>
              <div class="fact"><span>连接方式</span><strong>设备密钥 · 自动连接</strong></div>
              <div class="fact"><span>文件权限</span><strong>当前系统用户 · 全电脑</strong></div>
              <div class="fact"><span>托盘状态</span><strong id="tray-state">读取中…</strong></div>
            </div>
            <p class="hint" style="margin-top:12px">客户端名称可在“本地配置”中修改；管理员备注只在 Web 后台维护，两者互不覆盖。</p>
          </div>
        </section>

        <section id="tab-working" class="section">
          <div class="panel">
            <h2>任务与进度</h2>
            <p class="copy">绑定项目内的结构化 Plan 与 Markdown Workspace。这里直接使用 Node Working Context，不创建额外任务状态。</p>
            <div class="grid">
              <label class="field full"><span>当前项目</span><input id="working-project" maxlength="4096" placeholder="项目绝对路径"></label>
              <label class="field"><span>planId</span><input id="working-plan" maxlength="128" value="default"></label>
              <label class="field"><span>目标版本</span><input id="working-target" maxlength="128" placeholder="例如 0.4.1"></label>
            </div>
            <div class="actions"><button id="working-init" class="primary" type="button">初始化 / 绑定 docs/progress</button><button id="working-refresh" class="secondary" type="button">刷新</button><button id="working-sync" class="secondary" type="button" disabled>同步受管区块</button><button id="working-open" class="secondary" type="button" disabled>打开 Markdown 文件夹</button></div>
            <div class="facts">
              <div class="fact"><span>项目 / Plan</span><strong id="working-binding">未绑定</strong></div>
              <div class="fact"><span>目标版本</span><strong id="working-version">—</strong></div>
              <div class="fact"><span>Git</span><strong id="working-git">—</strong></div>
              <div class="fact"><span>总完成度</span><strong id="working-completion">—</strong><div class="progress-track"><div id="working-progress" class="progress-fill"></div></div></div>
              <div class="fact"><span>Markdown Workspace</span><strong id="working-workspace">未初始化</strong></div>
              <div class="fact"><span>Working Context revision</span><strong id="working-revision" class="mono">—</strong></div>
            </div>
	          </div>
	          <div class="panel">
            <div class="split">
              <div><h3 class="subhead">当前任务</h3><div id="working-current-tasks" class="task-list"><span class="empty">暂无</span></div></div>
              <div><h3 class="subhead">阻塞任务</h3><div id="working-blocked-tasks" class="task-list"><span class="empty">暂无</span></div></div>
            </div>
          </div>
          <div class="panel">
            <h2>更新任务</h2>
            <p class="copy">更新使用当前 revision；若内容已经变化，会要求刷新后重试。</p>
            <div class="grid">
              <label class="field full"><span>任务</span><select id="working-task"><option value="">暂无任务</option></select></label>
              <label class="field"><span>状态</span><select id="working-task-status"><option value="pending">待处理</option><option value="in_progress">进行中</option><option value="blocked">阻塞</option><option value="done">完成</option></select></label>
              <label class="field"><span>完成度（0–100）</span><input id="working-task-completion" type="number" min="0" max="100" value="0"></label>
            </div>
            <div class="actions"><button id="working-task-save" class="primary" type="button" disabled>保存任务状态</button></div>
            <div class="grid" style="margin-top:18px">
              <label class="field full"><span>验收证据摘要</span><input id="working-evidence-summary" maxlength="2048" placeholder="简短、可验证的验收结果"></label>
              <label class="field"><span>类型</span><input id="working-evidence-kind" maxlength="128" placeholder="test / review"></label>
              <label class="field"><span>引用</span><input id="working-evidence-reference" maxlength="2048" placeholder="测试命令、相对文件或工单"></label>
            </div>
            <div class="actions"><button id="working-evidence-add" class="secondary" type="button" disabled>添加验收证据</button></div>
            <h3 class="subhead" style="margin-top:20px">最近验收证据</h3><div id="working-evidences" class="task-list"><span class="empty">暂无</span></div>
          </div>
          <div class="panel">
            <h2>Markdown Workspace</h2>
            <p class="copy">只读查看已绑定项目 MarkdownRoot 内的普通 .md 文件。</p>
            <div class="workspace"><div id="working-files" class="file-list"><span class="empty">请先刷新</span></div><pre id="working-markdown" class="markdown-view">选择一个 Markdown 文件查看内容。</pre></div>
          </div>
        </section>

		<section id="tab-ai" class="section">
		  <div class="panel">
			<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:14px;flex-wrap:wrap"><div><h2>AI 与路由</h2><p class="copy">只读取本机 Codex、Claude Code 与 CC Switch 的脱敏状态，不会自动发起模型生成。</p></div><button id="ai-refresh" class="secondary" type="button">手动刷新</button></div>
			<div class="notice" id="ai-health-note">真实模型健康测试需用户手动从会话执行；本页面不会自动消耗模型额度。</div>
		  </div>
		  <div class="provider-grid">
			<div class="panel">
			  <h2>Codex</h2><p class="copy">本机 Codex runtime 与有效路由。</p>
			  <div class="facts"><div class="fact"><span>Runtime</span><strong id="ai-codex-runtime">待刷新</strong></div><div class="fact"><span>Version</span><strong id="ai-codex-version">—</strong></div><div class="fact"><span>Execution health</span><strong id="ai-codex-health">—</strong></div></div>
			  <h3 class="subhead" style="margin-top:18px">Route</h3><div id="ai-codex-route" class="data-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Models</h3><div id="ai-codex-models" class="tag-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Effective capabilities</h3><div id="ai-codex-capabilities" class="data-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Supported actions</h3><div id="ai-codex-actions" class="tag-list"><span class="empty">待刷新</span></div>
			</div>
			<div class="panel">
			  <h2>Claude Code</h2><p class="copy">仅显示认证是否已配置，不读取或展示凭据。</p>
			  <div class="facts"><div class="fact"><span>Runtime</span><strong id="ai-claude-runtime">待刷新</strong></div><div class="fact"><span>Version</span><strong id="ai-claude-version">—</strong></div><div class="fact"><span>Auth configured</span><strong id="ai-claude-auth">—</strong></div><div class="fact"><span>Execution health</span><strong id="ai-claude-health">—</strong></div></div>
			  <h3 class="subhead" style="margin-top:18px">Route</h3><div id="ai-claude-route" class="data-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Models</h3><div id="ai-claude-models" class="tag-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Effective capabilities</h3><div id="ai-claude-capabilities" class="data-list"><span class="empty">待刷新</span></div>
			  <h3 class="subhead" style="margin-top:18px">Supported actions</h3><div id="ai-claude-actions" class="tag-list"><span class="empty">待刷新</span></div>
			</div>
		  </div>
		  <div class="panel" style="margin-top:16px">
			<h2>CC Switch</h2><p class="copy">数据库只读发现、schema fingerprint、接管状态与当前 Provider 映射。</p>
			<div class="facts"><div class="fact"><span>DB detected</span><strong id="ai-cc-db">待刷新</strong></div><div class="fact"><span>Schema supported</span><strong id="ai-cc-schema">—</strong></div><div class="fact"><span>Schema fingerprint</span><strong id="ai-cc-fingerprint" class="mono">—</strong></div><div class="fact"><span>Proxy enabled</span><strong id="ai-cc-proxy">—</strong></div><div class="fact"><span>Takeover / Live</span><strong id="ai-cc-takeover">—</strong></div><div class="fact"><span>Selection consistent</span><strong id="ai-cc-selection">—</strong></div></div>
			<div class="split" style="margin-top:18px"><div><h3 class="subhead">Current Provider / Health</h3><div id="ai-cc-provider" class="data-list"><span class="empty">待刷新</span></div></div><div><h3 class="subhead">Model mapping</h3><div id="ai-cc-models" class="tag-list"><span class="empty">待刷新</span></div></div></div>
			<h3 class="subhead" style="margin-top:18px">Effective capabilities</h3><div id="ai-cc-capabilities" class="data-list"><span class="empty">待刷新</span></div>
		  </div>
		</section>

		<section id="tab-diagnostics" class="section">
		  <div class="panel">
			<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:14px;flex-wrap:wrap"><div><h2>诊断中心</h2><p class="copy">汇总本机 Node、Hub、Agent、任务工作区和本地能力的脱敏只读状态，不读取日志原文或敏感配置。</p></div><button id="diagnostics-refresh" class="secondary" type="button">刷新诊断</button></div>
			<div class="notice">刷新只执行本地 discovery、plan.get 与 markdown.list，不会创建会话、发送 Prompt 或运行真实模型健康测试。</div>
		  </div>
		  <div class="split">
			<div class="panel"><h2>Node / 客户端</h2><div id="diagnostics-node" class="data-list"><span class="empty">切换到本页后读取</span></div></div>
			<div class="panel"><h2>Hub 连接</h2><div id="diagnostics-hub" class="data-list"><span class="empty">切换到本页后读取</span></div></div>
		  </div>
		  <div class="panel">
			<h2>Agent runtime</h2><p class="copy">只显示 runtime、版本、认证状态、当前路由与统一 errorClass。</p>
			<div class="provider-grid"><div><h3 class="subhead">Codex</h3><div id="diagnostics-codex" class="data-list"><span class="empty">待刷新</span></div></div><div><h3 class="subhead">Claude Code</h3><div id="diagnostics-claude" class="data-list"><span class="empty">待刷新</span></div></div></div>
			<h3 class="subhead" style="margin-top:20px">CC Switch</h3><div id="diagnostics-ccswitch" class="data-list"><span class="empty">待刷新</span></div>
		  </div>
		  <div class="split">
			<div class="panel"><h2>Working Context / Task Workspace</h2><div id="diagnostics-workspace" class="data-list"><span class="empty">待刷新</span></div></div>
			<div class="panel"><h2>本地能力</h2><div id="diagnostics-local" class="data-list"><span class="empty">待刷新</span></div></div>
		  </div>
		  <div class="panel"><h2>最近诊断错误</h2><div id="diagnostics-errors" class="data-list"><span class="empty">暂无公开错误</span></div></div>
		  <div class="panel"><h2>脱敏诊断摘要</h2><p class="copy">该摘要由 allowlist DTO 生成，可直接选中复制。</p><pre id="diagnostics-summary" class="markdown-view" style="min-height:150px;max-height:320px">切换到本页后生成摘要。</pre></div>
		</section>

		<section id="tab-components" class="section">
		  <div class="panel">
			<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:14px;flex-wrap:wrap"><div><h2>本地组件中心</h2><p class="copy">只管理 Fast Spider 已知并经过校验的 Browser 与 ripgrep 组件；安装和更新始终需要手动触发。</p></div><button id="components-refresh" class="secondary" type="button">刷新状态</button></div>
			<div class="notice">组件状态不会显示安装路径、Hub 地址或凭据，也不会在页面加载时自动下载。</div>
		  </div>
			  <div class="provider-grid">
				<div class="panel"><h2>Browser</h2><p class="copy">受管浏览器运行时与 Sidecar。安装成功后沿用现有本地配置刷新行为。</p><div id="component-browser-data" class="data-list"><span class="empty">切换到本页后读取</span></div><div class="actions"><button id="browser-install" class="primary" type="button">安装 / 更新 Browser</button><span id="browser-status" class="hint">仅在点击后联网安装。</span></div></div>
				<div class="panel"><h2>ripgrep 搜索引擎</h2><p class="copy">code.search 优先使用已校验的受管 ripgrep；缺失时安全回退至 Go native。</p><div id="component-ripgrep-data" class="data-list"><span class="empty">切换到本页后读取</span></div><div class="actions"><button id="ripgrep-install" class="primary" type="button">安装 / 更新 ripgrep</button><span id="ripgrep-status" class="hint">安装后下次搜索自动生效。</span></div></div>
			  </div>
			  <div class="panel">
			<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:14px;flex-wrap:wrap"><div><h2>搜索与文件能力</h2><p class="copy">展示搜索引擎就绪状态，以及 file_read / file_edit 2.0 的本地能力摘要。</p></div><button id="search-file-self-test" class="secondary" type="button">运行本地自检</button></div>
			<div id="search-file-status" class="data-list" style="margin-top:16px"><span class="empty">切换到本页后读取</span></div>
			<div class="notice">自检只在 NodeUI 数据目录内创建并清理隔离临时文件；不会读取项目文件、下载组件或执行 AI。</div>
			<pre id="search-file-self-test-result" class="markdown-view" style="min-height:110px;max-height:240px">尚未运行。仅在点击按钮后执行。</pre>
		  </div>
		</section>

        <section id="tab-config" class="section">
          <div class="panel">
            <h2>本地配置</h2>
            <p class="copy">这些设置只保存在这台电脑，不同步到 Hub。Fast Spider 直接以当前系统用户身份操作本机文件、Shell、Git 和本地能力，不需要额外登记目录。</p>
            <form id="config-form">
              <div class="grid">
                <label class="field"><span>客户端名称</span><input id="config-name" maxlength="128" required><small class="hint">保存后会自动同步到 Hub；管理员备注只在 Web 后台维护。</small></label>
                <label class="switch"><input id="config-bridge" type="checkbox"><span><strong>Local Bridge</strong><br><small class="hint">允许当前系统用户的本地 AI 客户端调用 Node。</small></span></label>
				<label class="switch"><input id="config-codex-session-shared" name="config-codex-session-mode" type="radio" value="shared"><span><strong>Codex 共享模式（推荐）</strong><br><small class="hint">不让 FS 通过 Codex Desktop IPC 认领已加载会话，避免 Desktop 显示“已在另一个应用中打开”。</small></span></label>
				<label class="switch"><input id="config-codex-session-managed" name="config-codex-session-mode" type="radio" value="managed"><span><strong>Codex FS 接管模式</strong><br><small class="hint">启用 Desktop owner/control 联动；适合需要 FS 接管已加载本地会话的场景。</small></span></label>
                <label class="switch"><input id="config-autostart" type="checkbox"><span><strong>登录 Windows 后自动启动</strong><br><small class="hint">登录后隐藏启动到系统托盘，不弹出配置页面；仍然是同一个 EXE。</small></span></label>
                <label class="switch"><input id="config-autoupdate" type="checkbox"><span><strong>自动更新</strong><br><small class="hint">后台检查并下载新版本；下次启动时自动完成替换。</small></span></label>
              </div>
              <details class="advanced"><summary>高级 / 开发环境选项</summary><div class="grid" style="margin-top:10px"><label class="field full"><span>浏览器 Sidecar 目录</span><input id="config-browser" maxlength="4096" placeholder="正常无需填写，Browser 组件安装后会自动配置"><small class="hint">只在本地开发或自定义 Sidecar 时手工设置。</small></label><label class="switch full"><input id="config-insecure" type="checkbox"><span><strong>允许本机开发 HTTP Hub</strong><br><small class="hint">正式环境保持关闭，只使用 HTTPS。</small></span></label></div></details>
              <div class="actions"><button class="primary" type="submit">保存本地配置</button><span id="data-dir" class="hint mono"></span></div>
	            </form>
	          </div>
	          <div class="panel">
				<h2>ChatGPT Cloud Advanced</h2>
				<p class="copy">Preset 继续使用 ChatGPT 实时模型预设。这里只维护本机 Advanced 模型列表；Quick chat 与等待首个回答仍可分别搭配 Preset 或 Advanced。</p>
				<div class="notice">思考档位在每次读取与保存时从 ChatGPT Cloud 实时模型目录确认。Auto 表示不发送 thinking_effort；模型别名最终可能被服务端解析为其他 resolved model。</div>
				<form id="chatgpt-advanced-form">
				  <div id="chatgpt-advanced-list" class="advanced-model-list"><span class="empty">切换到本页后读取</span></div>
				  <div class="actions"><button id="chatgpt-advanced-add" class="secondary" type="button">新增模型</button><button class="primary" type="submit">保存 Advanced 列表</button><span id="chatgpt-advanced-file" class="hint mono"></span></div>
				</form>
	          </div>
	          <div class="panel">
			<h2>版本更新</h2>
			<p class="copy">主程序更新包由当前 Hub 签名，并同时校验 SHA256；扩展能力请在“组件”页管理。</p>
            <div class="facts">
              <div class="fact"><span>当前版本</span><strong id="update-current">{{VERSION}}</strong></div>
              <div class="fact"><span>最新版本</span><strong id="update-latest">尚未检查</strong></div>
              <div class="fact"><span>更新状态</span><strong id="update-state">尚未检查</strong></div>
            </div>
            <div class="actions"><button id="update-check" class="secondary" type="button">检查更新</button><button id="update-install" class="primary" type="button" disabled>立即升级</button><span id="update-time" class="hint"></span></div>
          </div>
        </section>

		<section id="tab-operation-logs" class="section">
		  <div class="panel">
			<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:14px;flex-wrap:wrap"><div><h2>操作日志</h2><p class="copy">记录本机客户端的 API 调用、浏览器操作、Hub 连接等事件。日志仅保存在本地，自动保留最近 7 天。</p></div><div style="display:flex;gap:8px"><button id="oplog-refresh" class="secondary" type="button">刷新日志</button><button id="oplog-cleanup" class="secondary" type="button">清理过期</button></div></div>
			<div class="facts" style="margin-top:0">
			  <div class="fact"><span>总条数</span><strong id="oplog-total">—</strong></div>
			  <div class="fact"><span>保留天数</span><strong id="oplog-retention">7 天</strong></div>
			  <div class="fact"><span>最早记录</span><strong id="oplog-oldest">—</strong></div>
			  <div class="fact"><span>最新记录</span><strong id="oplog-newest">—</strong></div>
			  <div class="fact"><span>Info / Warn / Error</span><strong id="oplog-levels">—</strong></div>
			  <div class="fact"><span>分类</span><strong id="oplog-cats">—</strong></div>
			</div>
		  </div>
		  <div class="panel">
			<div class="grid" style="margin-bottom:14px">
			  <label class="field"><span>级别过滤</span><select id="oplog-level"><option value="">全部</option><option value="info">Info</option><option value="warning">Warning</option><option value="error">Error</option></select></label>
			  <label class="field"><span>分类过滤</span><select id="oplog-category"><option value="">全部</option></select></label>
			  <label class="field"><span>每页条数</span><select id="oplog-limit"><option value="50">50</option><option value="100" selected>100</option><option value="200">200</option><option value="500">500</option></select></label>
			</div>
			<div id="oplog-list" class="data-list" style="max-height:520px;overflow:auto;padding-right:6px"><span class="empty">切换到本页后加载日志…</span></div>
			<div style="display:flex;justify-content:space-between;align-items:center;margin-top:12px;gap:10px;flex-wrap:wrap">
			  <span id="oplog-page-info" class="hint">—</span>
			  <div style="display:flex;gap:8px"><button id="oplog-prev" class="secondary" type="button" disabled>上一页</button><button id="oplog-next" class="secondary" type="button" disabled>下一页</button></div>
			</div>
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
  let workingDirty = false;
  let workingBusy = false;
  let workingState = null;
  let workingRevision = '';
	let aiBusy = false;
	let diagnosticsBusy = false;
	let componentsBusy = false;
	let selfTestBusy = false;
	let oplogBusy = false;
	let oplogOffset = 0;
	let oplogTotal = 0;
	let chatGPTAdvancedBusy = false;
	let chatGPTThinkingOptions = [];

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
    $('registration-panel').hidden = !!status.registered;
    $('registered-panel').hidden = !status.registered;
    $('machine-name').textContent = (status.config && status.config.machineName) || '—';
    $('machine-hub').textContent = status.hubUrl || '—';
    $('tray-state').textContent = status.traySupported ? (status.trayActive ? '已驻留 · 右键可退出' : '未启动') : '当前系统不支持';
    $('data-dir').textContent = '配置目录：' + status.dataDir;
    $('exit-app').textContent = status.runtimeOwned ? '退出客户端' : '关闭界面';
    if (status.runtimeStatus === 'external_running' && status.runtimeError) message(status.runtimeError);

    const cfg = status.config || {};
		$('codex-session-mode-panel').hidden = !!cfg.codexDesktopBridgeConfigured;
		const codexManaged = !!cfg.codexDesktopBridgeEnabled;
		document.querySelector('input[name="startup-codex-session-mode"][value="' + (codexManaged ? 'managed' : 'shared') + '"]').checked = true;
    if (!status.registered && (!document.activeElement || !['connect-hub','connect-name','connect-token'].includes(document.activeElement.id))) {
      $('connect-hub').value = cfg.hubUrl || '';
      $('connect-name').value = cfg.machineName || '';
    }
    if (!configDirty) {
      $('config-name').value = cfg.machineName || '';
      $('config-browser').value = cfg.browserSidecarDir || '';
      $('config-bridge').checked = !!cfg.localBridgeEnabled;
      $('config-autostart').checked = !!status.autoStartEnabled;
      $('config-autoupdate').checked = !!cfg.autoUpdateEnabled;
      $('config-insecure').checked = !!cfg.allowInsecureLocalHub;
		$('config-codex-session-shared').checked = !codexManaged;
		$('config-codex-session-managed').checked = codexManaged;
    }
    if (!workingDirty) {
      $('working-project').value = cfg.workingProjectPath || '';
      $('working-plan').value = cfg.workingPlanId || 'default';
    }
    $('config-autostart').disabled = !status.autoStartSupported;
    renderUpdate(status.update || {});
  }

  function renderUpdate(update) {
    $('update-current').textContent = update.currentVersion || '{{VERSION}}';
    $('update-latest').textContent = update.latestVersion || '尚未检查';
    let state = '尚未检查';
    if (update.checking) state = '正在检查';
    else if (update.error) state = '检查失败';
    else if (update.waitingForIdle) state = '已下载，等待当前任务结束';
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

  function workingArgs(action, extra = {}) {
    return Object.assign({action:action,projectPath:$('working-project').value.trim(),planId:$('working-plan').value.trim()}, extra);
  }

  async function workingCall(action, extra = {}) {
    return api('/api/working',{method:'POST',body:JSON.stringify(workingArgs(action,extra))});
  }

  function taskRows(tasks, emptyText) {
    if (!tasks.length) return '<span class="empty">' + emptyText + '</span>';
    return tasks.map(task => '<div class="task-row"><strong>' + escapeText(task.id + ' · ' + task.title) + '</strong><small>' + escapeText(task.status + ' · ' + task.completion + '%' + (task.blockedReason ? ' · ' + task.blockedReason : '')) + '</small></div>').join('');
  }

  function escapeText(value) {
    const span=document.createElement('span'); span.textContent=value || ''; return span.innerHTML;
  }
	function escapeAttr(value) {
    return escapeText(value).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

	function boolText(value) { return value === true ? '是' : value === false ? '否' : '—'; }
	function renderTags(id, values, formatter) {
	  const box=$(id); box.textContent='';
	  if(!Array.isArray(values) || !values.length){const empty=document.createElement('span');empty.className='empty';empty.textContent='暂无';box.appendChild(empty);return;}
	  values.forEach(value=>{const tag=document.createElement('span');tag.className='tag';tag.textContent=formatter ? formatter(value) : String(value);box.appendChild(tag);});
	}
	function advancedModelRow(model) {
	  const row=document.createElement('div'); row.className='advanced-model-row';
	  const fields=document.createElement('div'); fields.className='advanced-model-fields';
	  const idLabel=document.createElement('label'); idLabel.className='field'; idLabel.innerHTML='<span>模型 ID</span><input class="advanced-model-id" maxlength="256" required placeholder="gpt-5.6-terra-wm">'; idLabel.querySelector('input').value=model.id || '';
	  const titleLabel=document.createElement('label'); titleLabel.className='field'; titleLabel.innerHTML='<span>显示名称</span><input class="advanced-model-title" maxlength="128" required placeholder="GPT-5.6 Terra">'; titleLabel.querySelector('input').value=model.title || '';
	  const remove=document.createElement('button'); remove.type='button'; remove.className='danger'; remove.textContent='删除'; remove.addEventListener('click',()=>row.remove());
	  fields.append(idLabel,titleLabel,remove); row.appendChild(fields);
	  const thinking=document.createElement('div'); thinking.className='thinking-list'; const selected=new Set(model.thinking || []);
	  chatGPTThinkingOptions.forEach(option=>{const label=document.createElement('label');label.className='switch';const input=document.createElement('input');input.type='checkbox';input.className='advanced-thinking';input.value=option.id;input.checked=selected.has(option.id);const text=document.createElement('span');text.textContent=option.title+(option.source==='chatgpt_cloud'?' · 官方':' · 默认');label.append(input,text);thinking.appendChild(label);});
	  row.appendChild(thinking); return row;
	}
	function renderChatGPTAdvanced(data) {
	  chatGPTThinkingOptions=Array.isArray(data.thinkingOptions)?data.thinkingOptions:[]; const box=$('chatgpt-advanced-list'); box.textContent='';
	  const models=Array.isArray(data.models)?data.models:[]; models.forEach(model=>box.appendChild(advancedModelRow(model)));
	  if(!models.length){const empty=document.createElement('span');empty.className='empty';empty.textContent='尚未配置 Advanced 模型，可点击“新增模型”。';box.appendChild(empty);}
	  $('chatgpt-advanced-file').textContent=data.configFile ? '配置文件：'+data.configFile : '';
	}
	async function refreshChatGPTAdvanced() {
	  if(chatGPTAdvancedBusy)return; chatGPTAdvancedBusy=true;
	  try{renderChatGPTAdvanced(await api('/api/chatgpt-advanced-models'));}catch(e){message(e.message,true);}finally{chatGPTAdvancedBusy=false;}
	}
	function renderData(id, rows) {
	  const box=$(id); box.textContent='';
	  if(!rows.length){const empty=document.createElement('span');empty.className='empty';empty.textContent='暂无';box.appendChild(empty);return;}
	  rows.forEach(row=>{const line=document.createElement('div');line.className='data-row';const label=document.createElement('span');label.textContent=row[0];const value=document.createElement('strong');value.textContent=row[1] || '—';line.append(label,value);box.appendChild(line);});
	}
	function renderCapabilities(id, capabilities) {
	  renderData(id,Object.entries(capabilities || {}).map(([name,item])=>[name,(item.state || 'unknown')+(item.reason ? ' · '+item.reason : '')]));
	}
	function modelLabel(model) { return model.displayName || model.id || model.model || model.upstreamModel || '未知模型'; }
	function renderRoute(id, route) {
	  route=route || {};
	  renderData(id,[['状态',route.available ? '可用' : (route.reason || '不可用')],['模式',route.routingMode || '—'],['当前 Provider',route.currentProvider || '—'],['选择一致',route.selectionConsistent === undefined ? '—' : boolText(route.selectionConsistent)],['错误',route.errorClass ? route.errorClass+' · '+(route.errorMessage || '') : '无公开错误']]);
	}
	function renderAI(data) {
	  const codex=data.codex || {}, claude=data.claudeCode || {}, cc=data.ccSwitch || {};
	  $('ai-codex-runtime').textContent=(codex.available ? 'available' : 'unavailable'); $('ai-codex-version').textContent=codex.version || '—'; $('ai-codex-health').textContent=codex.executionHealth || '—';
	  renderRoute('ai-codex-route',codex.route); renderTags('ai-codex-models',codex.models,modelLabel); renderCapabilities('ai-codex-capabilities',codex.effectiveCapabilities); renderTags('ai-codex-actions',codex.supportedActions);
	  $('ai-claude-runtime').textContent=(claude.available ? 'available' : 'unavailable'); $('ai-claude-version').textContent=claude.version || '—'; $('ai-claude-auth').textContent=claude.authStatus || boolText(claude.authConfigured); $('ai-claude-health').textContent=claude.executionHealth || '—';
	  renderRoute('ai-claude-route',claude.route); renderTags('ai-claude-models',claude.models,modelLabel); renderCapabilities('ai-claude-capabilities',claude.effectiveCapabilities); renderTags('ai-claude-actions',claude.supportedActions);
	  $('ai-cc-db').textContent=boolText(cc.dbDetected); $('ai-cc-schema').textContent=boolText(cc.schemaSupported)+(cc.reason ? ' · '+cc.reason : ''); $('ai-cc-fingerprint').textContent=cc.schemaFingerprint || '—'; $('ai-cc-proxy').textContent=boolText(cc.proxyEnabled); $('ai-cc-takeover').textContent=boolText(cc.takeover)+' / '+boolText(cc.liveTakeover); $('ai-cc-selection').textContent=boolText(cc.selectionConsistent);
	  const health=Array.isArray(cc.providerHealth) ? cc.providerHealth : []; renderData('ai-cc-provider',[['Provider',cc.currentProvider || '—'],['Health',health.length ? health.map(item=>(item.healthy?'healthy':'unhealthy')+' · failures '+item.consecutiveFailures).join(' · ') : '—'],['错误',cc.errorClass ? cc.errorClass+' · '+(cc.errorMessage || '') : '无公开错误']]);
	  renderTags('ai-cc-models',cc.modelMapping,modelLabel); renderCapabilities('ai-cc-capabilities',cc.effectiveCapabilities); $('ai-health-note').textContent=(data.healthTest && data.healthTest.message) || '真实模型健康测试需用户手动从会话执行。';
	}
	async function refreshAI() {
	  if(aiBusy)return; aiBusy=true; $('ai-refresh').disabled=true;
	  try{renderAI(await api('/api/ai-routing'));message('AI 与路由状态已刷新。');}catch(e){message(e.message,true);}finally{aiBusy=false;$('ai-refresh').disabled=false;}
	}
	function diagnosticRuntimeRows(runtime, includeAuth) {
	  runtime=runtime || {}; const rows=[['Runtime',runtime.runtime || 'unknown'],['Version',runtime.version || '—'],['Route',runtime.route || 'unknown']];
	  if(runtime.readyForSessionCreate !== undefined) rows.push(['可创建新会话',boolText(runtime.readyForSessionCreate)],['Readiness',String(runtime.readinessReasonCode || 'unknown')+' · '+String(runtime.readinessMs || 0)+'ms']);
	  if(includeAuth) rows.push(['Auth configured',boolText(runtime.authConfigured)]);
	  rows.push(['错误',runtime.errorClass ? runtime.errorClass+' · '+(runtime.errorMessage || '') : '无公开错误']); return rows;
	}
	function renderDiagnostics(data) {
	  const node=data.node || {}, hub=data.hub || {}, agent=data.agent || {}, workspace=data.workspace || {}, local=data.local || {}, cc=(agent.ccSwitch || {});
	  renderData('diagnostics-node',[['版本',node.version || 'unknown'],['配置',node.configStatus || 'unknown'],['已登记',boolText(node.registered)],['Runtime',node.runtimeStatus || 'unknown'],['本地进程拥有',boolText(node.runtimeOwned)],['自动启动',boolText(node.autoStartEnabled)],['自动更新',boolText(node.autoUpdateEnabled)]]);
	  renderData('diagnostics-hub',[['已配置',boolText(hub.configured)],['Hub host',hub.host || '—'],['连接状态',hub.connectionStatus || 'unknown'],['最近状态',hub.lastKnownStatus || 'unknown']]);
	  renderData('diagnostics-codex',diagnosticRuntimeRows(agent.codex,false)); renderData('diagnostics-claude',diagnosticRuntimeRows(agent.claudeCode,true));
	  renderData('diagnostics-ccswitch',[['DB detected',boolText(cc.dbDetected)],['Schema supported',boolText(cc.schemaSupported)],['Schema fingerprint',cc.schemaFingerprint || '—'],['Current route',cc.currentRoute || 'unknown'],['Selection consistent',boolText(cc.selectionConsistent)],['错误',cc.errorClass ? cc.errorClass+' · '+(cc.errorMessage || '') : '无公开错误']]);
	  renderData('diagnostics-workspace',[['绑定',boolText(workspace.bound)],['项目摘要',workspace.projectStatus || 'not_bound'],['Plan',workspace.planId || '—'],['可读',boolText(workspace.readable)],['计划存在',boolText(workspace.exists)],['Revision',workspace.revision || '—'],['Markdown 文件',String(workspace.markdownFiles || 0)]]);
	  renderData('diagnostics-local',[['Local Bridge configured',boolText(local.localBridgeConfigured)],['Browser configured',boolText(local.browserConfigured)],['Browser present',boolText(local.browserPresent)],['Browser ready',boolText(local.browserReady)],['Browser readiness',String(local.browserReasonCode || 'unknown')+' · '+String(local.browserReadinessMs || 0)+'ms'],['WSL runtime',boolText(local.wslAvailable)],['Component root present',boolText(local.componentRootPresent)],['Tray supported',boolText(local.traySupported)],['Tray active',boolText(local.trayActive)]]);
	  renderData('diagnostics-errors',(data.errors || []).map(item=>[item.area || 'diagnostic',(item.errorClass || 'unknown')+' · '+(item.publicMessage || '诊断失败')]));
	  const summary=data.summary || {}; $('diagnostics-summary').textContent=['Node: '+(summary.node || '—'),'Hub: '+(summary.hub || '—'),'Agent: '+(summary.agent || '—'),'Workspace: '+(summary.workspace || '—'),'Local: '+(summary.local || '—')].join('\n');
	}
	async function refreshDiagnostics() {
	  if(diagnosticsBusy)return; diagnosticsBusy=true; $('diagnostics-refresh').disabled=true;
	  try{renderDiagnostics(await api('/api/diagnostics'));message('诊断状态已刷新。');}catch(e){message(e.message,true);}finally{diagnosticsBusy=false;$('diagnostics-refresh').disabled=false;}
	}
	function componentRows(component) {
	  return [['状态',component.status || 'unknown'],['已安装',boolText(component.installed)],['版本',component.version || '—'],['平台',component.platform || 'unknown'],['可执行就绪',boolText(component.executableReady)],['引擎就绪',boolText(component.engineReady)]];
	}
	function renderSearchFileStatus(data) {
	  const read=data.fileRead || {}, edit=data.fileEdit || {};
	  renderData('search-file-status',[
		['搜索引擎',data.searchEngine || 'native'],['ripgrep installed',boolText(data.ripgrepInstalled)],['ripgrep verified',boolText(data.ripgrepVerified)],['native fallback',boolText(data.nativeReady)],
		['file_read',(read.version || 'unknown')+' · '+(read.actions || []).join(', ')],['file_edit',(edit.version || 'unknown')+' · '+(edit.actions || []).join(', ')],
	  ]);
	}
		async function refreshComponents() {
		  if(componentsBusy)return; componentsBusy=true; $('components-refresh').disabled=true;
		  try {
			const [components,capabilities]=await Promise.all([api('/api/components'),api('/api/search-file/status')]);
			const byId={}; (components.components || []).forEach(item=>{byId[item.id]=item;});
			renderData('component-browser-data',componentRows(byId.browser || {})); renderData('component-ripgrep-data',componentRows(byId['search-ripgrep'] || {})); renderSearchFileStatus(capabilities);
		  } catch(e) { message(e.message,true); }
		  finally { componentsBusy=false; $('components-refresh').disabled=false; }
		}
	async function ensureComponent(componentId,buttonId,statusId) {
	  if(componentsBusy)return; componentsBusy=true; $(buttonId).disabled=true; $(statusId).textContent='正在下载并校验组件…';
	  try { const data=await api('/api/components/ensure',{method:'POST',body:JSON.stringify({componentId})}); $(statusId).textContent=(data.component && data.component.version ? data.component.version : '组件')+' 已安装'; message('组件安装完成。'); }
	  catch(e) { $(statusId).textContent='安装失败'; message(e.message,true); }
	  finally { componentsBusy=false; $(buttonId).disabled=false; await refreshComponents(); }
	}
	async function runSearchFileSelfTest() {
	  if(selfTestBusy)return; selfTestBusy=true; $('search-file-self-test').disabled=true; $('search-file-self-test-result').textContent='正在运行隔离的本地自检…';
	  try { const data=await api('/api/search-file/self-test',{method:'POST',body:'{}'}); $('search-file-self-test-result').textContent=['Status: '+data.status,'Engine: '+(data.engine || '—'),'Fallback: '+(data.fallbackReason || '—'),'Elapsed: '+String(data.elapsedMs || 0)+' ms','file_read: '+data.fileRead,'file_edit preview: '+data.fileEditPreview,'Error: '+(data.errorClass ? data.errorClass+' · '+(data.publicMessage || '') : '—')].join('\n'); message(data.status === 'PASS' ? '搜索与文件能力自检完成。' : (data.publicMessage || '本地自检未通过。'),data.status !== 'PASS'); }
	  catch(e) { $('search-file-self-test-result').textContent='FAIL\n'+e.message; message(e.message,true); }
	  finally { selfTestBusy=false; $('search-file-self-test').disabled=false; }
	}

  function renderWorking(result, markdown) {
    workingState = result.state || null;
    workingRevision = result.revision || '';
    const state = workingState || {};
    const tasks = Array.isArray(state.tasks) ? state.tasks : [];
    const currentTasks = tasks.filter(task => task.status === 'in_progress' || task.status === 'pending');
    const blockedTasks = tasks.filter(task => task.status === 'blocked');
    const completion = tasks.length ? Math.round(tasks.reduce((sum,task) => sum + Number(task.completion || 0),0) / tasks.length) : 0;
    const git = result.currentGit || {};
    $('working-binding').textContent = result.exists ? (state.planId || 'default') + ' · ' + (state.projectPath || $('working-project').value) : '未初始化';
    $('working-version').textContent = state.targetVersion || '—';
    $('working-git').textContent = git.isRepository ? ((git.branch || 'detached') + ' · ' + (git.head || '—').slice(0,12) + (git.dirty ? ' · dirty' : ' · clean')) : '非 Git 仓库';
    $('working-completion').textContent = completion + '%';
    $('working-progress').style.width = completion + '%';
    $('working-revision').textContent = workingRevision || '—';
    $('working-current-tasks').innerHTML = taskRows(currentTasks,'当前没有待处理任务');
    $('working-blocked-tasks').innerHTML = taskRows(blockedTasks,'当前没有阻塞任务');
    const select = $('working-task');
    const selected = select.value;
    select.textContent='';
    if(tasks.length){tasks.forEach(task=>{const option=document.createElement('option');option.value=task.id;option.textContent=task.id+' · '+task.title;select.appendChild(option);});}
    else{const option=document.createElement('option');option.value='';option.textContent='暂无任务';select.appendChild(option);}
    if (tasks.some(task => task.id === selected)) select.value=selected;
    const active = tasks.find(task => task.id === select.value);
    if (active) { $('working-task-status').value=active.status; $('working-task-completion').value=active.completion; }
    $('working-task-save').disabled = !result.exists || !tasks.length;
    $('working-evidence-add').disabled = !result.exists || !tasks.length;
    $('working-sync').disabled = !result.exists;
    $('working-open').disabled = !result.exists;
    const evidences=[];
    tasks.forEach(task => (task.evidences || []).forEach(item => evidences.push(Object.assign({taskId:task.id},item))));
    evidences.sort((a,b) => String(b.acceptedAt || '').localeCompare(String(a.acceptedAt || '')));
    $('working-evidences').innerHTML = evidences.length ? evidences.slice(0,8).map(item => '<div class="task-row"><strong>' + escapeText(item.taskId + ' · ' + item.summary) + '</strong><small>' + escapeText((item.kind || 'evidence') + (item.reference ? ' · ' + item.reference : '')) + '</small></div>').join('') : '<span class="empty">暂无验收证据</span>';
    renderWorkingFiles(markdown || []);
  }

  function renderWorkingFiles(files) {
    $('working-workspace').textContent = files.length ? ('正常 · ' + files.length + ' 个文件') : '未初始化或为空';
    const box=$('working-files'); box.textContent='';
    if (!files.length) { box.innerHTML='<span class="empty">暂无 Markdown 文件</span>'; return; }
    files.forEach(file => { const button=document.createElement('button'); button.type='button'; button.className='secondary'; button.textContent=file.path + ' · ' + file.size + ' B'; button.addEventListener('click',() => readWorkingMarkdown(file.path)); box.appendChild(button); });
  }

  async function refreshWorking() {
    if (workingBusy || !$('working-project').value.trim() || !$('working-plan').value.trim()) return;
    workingBusy=true;
    try {
      const result=await workingCall('plan.get');
      let files=[];
      if (result.exists) { const listed=await workingCall('markdown.list'); files=listed.markdown || []; }
      workingDirty=false; renderWorking(result,files); message(result.exists ? '任务与进度已刷新。' : '尚未初始化该计划。');
    } catch(e) { message(e.message,true); } finally { workingBusy=false; }
  }

  async function readWorkingMarkdown(path) {
    if (workingBusy) return; workingBusy=true;
    try { const result=await workingCall('markdown.read',{markdownPath:path}); $('working-markdown').textContent=result.content || ''; }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  }

  document.querySelectorAll('.nav button').forEach(button => button.addEventListener('click', () => {
    document.querySelectorAll('.nav button').forEach(x => x.classList.toggle('active', x === button));
    document.querySelectorAll('.section').forEach(x => x.classList.toggle('active', x.id === 'tab-' + button.dataset.tab));
    if (button.dataset.tab === 'working' && $('working-project').value.trim()) refreshWorking();
	if (button.dataset.tab === 'ai') refreshAI();
	if (button.dataset.tab === 'diagnostics') refreshDiagnostics();
		if (button.dataset.tab === 'components') refreshComponents();
		if (button.dataset.tab === 'config') refreshChatGPTAdvanced();
	  }));
	$('ai-refresh').addEventListener('click',refreshAI);
	$('diagnostics-refresh').addEventListener('click',refreshDiagnostics);
		$('components-refresh').addEventListener('click',refreshComponents);
	$('search-file-self-test').addEventListener('click',runSearchFileSelfTest);

  $('working-project').addEventListener('input',()=>{workingDirty=true;});
  $('working-plan').addEventListener('input',()=>{workingDirty=true;});
  $('working-target').addEventListener('input',()=>{workingDirty=true;});
  $('working-init').addEventListener('click',async()=>{
    if(workingBusy)return; workingBusy=true;
    try { const result=await workingCall('plan.init',{targetVersion:$('working-target').value.trim()}); const listed=await workingCall('markdown.list'); workingDirty=false; renderWorking(result,listed.markdown || []); message('项目计划已绑定，docs/progress 已验证或初始化。'); }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  });
  $('working-refresh').addEventListener('click',refreshWorking);
  $('working-task').addEventListener('change',()=>{ const task=(workingState && workingState.tasks || []).find(item=>item.id===$('working-task').value); if(task){$('working-task-status').value=task.status;$('working-task-completion').value=task.completion;} });
  $('working-task-save').addEventListener('click',async()=>{
    if(workingBusy)return; workingBusy=true;
    try { const result=await workingCall('task.update',{expectedRevision:workingRevision,taskId:$('working-task').value,taskStatus:$('working-task-status').value,completion:Number($('working-task-completion').value)}); const listed=await workingCall('markdown.list'); renderWorking(result,listed.markdown || []); message('任务状态已更新。'); }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  });
  $('working-evidence-add').addEventListener('click',async()=>{
    const summary=$('working-evidence-summary').value.trim(); if(!summary){message('请填写验收证据摘要。',true);return;} if(workingBusy)return; workingBusy=true;
    try { const result=await workingCall('task.update',{expectedRevision:workingRevision,taskId:$('working-task').value,evidence:{summary:summary,kind:$('working-evidence-kind').value.trim(),reference:$('working-evidence-reference').value.trim()}}); const listed=await workingCall('markdown.list'); renderWorking(result,listed.markdown || []); $('working-evidence-summary').value=''; $('working-evidence-kind').value=''; $('working-evidence-reference').value=''; message('验收证据已添加。'); }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  });
  $('working-sync').addEventListener('click',async()=>{
    if(workingBusy)return; workingBusy=true;
    try { await workingCall('plan.sync',{expectedRevision:workingRevision}); const result=await workingCall('plan.get'); const listed=await workingCall('markdown.list'); renderWorking(result,listed.markdown || []); message('受管区块已同步，Manual 内容保持不变。'); }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  });
  $('working-open').addEventListener('click',async()=>{
    if(workingBusy)return; workingBusy=true;
    try { await api('/api/working',{method:'POST',body:JSON.stringify({action:'folder.open'})}); message('已打开 Markdown 文件夹。'); }
    catch(e){message(e.message,true);} finally{workingBusy=false;}
  });

  $('toggle-token').addEventListener('click', () => {
    const input = $('connect-token'); input.type = input.type === 'password' ? 'text' : 'password'; $('toggle-token').textContent = input.type === 'password' ? '显示' : '隐藏';
  });

  $('connect-form').addEventListener('submit', async event => {
    event.preventDefault(); if (busy) return; busy = true; const submit = event.currentTarget.querySelector('button[type="submit"]'); submit.disabled = true; message('正在登记并连接这台设备…');
    try {
      const data = await api('/api/connect',{method:'POST',body:JSON.stringify({hubUrl:$('connect-hub').value,token:$('connect-token').value,machineName:$('connect-name').value})});
      $('connect-token').value=''; renderStatus(data); message('设备已登记。以后启动会自动连接，不再需要输入连接密钥。');
    } catch (e) { message(e.message,true); } finally { busy=false; submit.disabled=false; }
  });

  $('config-form').addEventListener('input', () => { configDirty = true; });
  $('config-form').addEventListener('change', () => { configDirty = true; });

	$('config-form').addEventListener('submit', async event => {
    event.preventDefault(); if (busy) return; busy=true; const submit = event.currentTarget.querySelector('button[type="submit"]'); submit.disabled=true;
    try {
      const data = await api('/api/config',{method:'POST',body:JSON.stringify(localConfigPayload($('config-codex-session-managed').checked, true))});
      configDirty = false; renderStatus(data); message('本地配置已保存。');
    } catch (e) { message(e.message,true); } finally { busy=false; submit.disabled=false; }
  });

	function localConfigPayload(codexDesktopBridgeEnabled, codexDesktopBridgeConfigured) {
		return {machineName:$('config-name').value,browserSidecarDir:$('config-browser').value,localBridgeEnabled:$('config-bridge').checked,autoStartEnabled:$('config-autostart').checked,autoUpdateEnabled:$('config-autoupdate').checked,allowInsecureLocalHub:$('config-insecure').checked,codexDesktopBridgeEnabled:codexDesktopBridgeEnabled,codexDesktopBridgeConfigured:codexDesktopBridgeConfigured};
	}

	$('codex-session-mode-form').addEventListener('submit', async event => {
		event.preventDefault(); if (busy) return; busy=true; const submit = event.currentTarget.querySelector('button[type="submit"]'); submit.disabled=true;
		try {
			const selected = document.querySelector('input[name="startup-codex-session-mode"]:checked').value === 'managed';
			const data = await api('/api/config',{method:'POST',body:JSON.stringify(localConfigPayload(selected, true))});
			configDirty = false; renderStatus(data); message(selected ? '已启用 FS 接管模式。' : '已启用 Codex 共享模式；FS 不会再认领本地 Desktop 会话。');
		} catch (e) { message(e.message,true); } finally { busy=false; submit.disabled=false; }
	});
	$('chatgpt-advanced-add').addEventListener('click',()=>{const box=$('chatgpt-advanced-list');const empty=box.querySelector('.empty');if(empty)empty.remove();box.appendChild(advancedModelRow({thinking:chatGPTThinkingOptions.map(option=>option.id)}));});
	$('chatgpt-advanced-form').addEventListener('submit',async event=>{
	  event.preventDefault();if(chatGPTAdvancedBusy)return;chatGPTAdvancedBusy=true;const submit=event.currentTarget.querySelector('button[type="submit"]');submit.disabled=true;
	  try{const models=Array.from(document.querySelectorAll('.advanced-model-row')).map(row=>({id:row.querySelector('.advanced-model-id').value.trim(),title:row.querySelector('.advanced-model-title').value.trim(),thinking:Array.from(row.querySelectorAll('.advanced-thinking:checked')).map(input=>input.value)}));const data=await api('/api/chatgpt-advanced-models',{method:'POST',body:JSON.stringify({version:1,models})});renderChatGPTAdvanced(data);message('ChatGPT Advanced 模型列表已保存到本机 Node。');}catch(e){message(e.message,true);}finally{chatGPTAdvancedBusy=false;submit.disabled=false;}
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

	$('browser-install').addEventListener('click',()=>ensureComponent('browser','browser-install','browser-status'));
	$('ripgrep-install').addEventListener('click',()=>ensureComponent('search-ripgrep','ripgrep-install','ripgrep-status'));

  $('exit-app').addEventListener('click', async () => {
    const ownsRuntime = current && current.runtimeOwned;
    const prompt = ownsRuntime ? '真正退出 Fast Spider Node？关闭浏览器页面不会退出；这里确认后会结束托盘和设备连接，MCP 将无法访问这台设备。' : '关闭本地界面？旧的无界面 Node 进程会继续运行。';
    if (!confirm(prompt)) return;
    try { await api('/api/exit',{method:'POST',body:'{}'}); document.body.textContent=ownsRuntime ? 'Fast Spider Node 已退出，可以关闭此窗口。' : '本地界面已关闭；旧的无界面 Node 仍在运行。'; } catch(e) { message(e.message,true); }
  });

  function formatLocalTime(ts) {
    if (!ts) return '\u2014';
    const d = new Date(ts);
    const pad = (n) => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }
  function levelBadgeClass(level) {
    return level === 'error' ? 'bad' : level === 'warning' ? 'warn' : 'ok';
  }
  function renderOperationLogs(data) {
    oplogTotal = data.total || 0;
    const entries = data.entries || [];
    const categories = data.categories || [];
    const catSelect = $('oplog-category');
    const currentCat = catSelect.value;
    catSelect.innerHTML = '<option value="">\u5168\u90e8</option>' + categories.map(c => '<option value="' + escapeAttr(c) + '"' + (c === currentCat ? ' selected' : '') + '>' + escapeText(c) + '</option>').join('');
    const list = $('oplog-list');
    if (!entries.length) {
      list.innerHTML = '<span class="empty">\u6682\u65e0\u64cd\u4f5c\u65e5\u5fd7</span>';
    } else {
      list.innerHTML = entries.map(entry => {
        const time = formatLocalTime(entry.timestamp);
        const levelCls = levelBadgeClass(entry.level);
        const extra = entry.status ? (' \u00b7 ' + entry.status + (entry.duration_ms ? ' \u00b7 ' + entry.duration_ms + 'ms' : '')) : '';
        const client = entry.client_ip ? (' \u00b7 ' + escapeText(entry.client_ip)) : '';
        const method = entry.method ? (entry.method + ' ') : '';
        return '<div class="task-row"><strong><span class="dot ' + levelCls + '" style="display:inline-block;vertical-align:middle;margin-right:6px"></span>' + escapeText(entry.category) + ' \u00b7 ' + escapeText(entry.action) + '</strong><small>' + escapeText(time) + client + '</small><div style="margin-top:4px;font-size:13px;color:var(--muted);word-break:break-word">' + escapeText(method + entry.message + extra) + '</div></div>';
      }).join('');
    }
    const limit = parseInt($('oplog-limit').value) || 100;
    const pageStart = oplogTotal ? oplogOffset + 1 : 0;
    const pageEnd = Math.min(oplogOffset + limit, oplogTotal);
    $('oplog-page-info').textContent = oplogTotal ? ('\u7b2c ' + pageStart + '\u2013' + pageEnd + ' \u6761 / \u5171 ' + oplogTotal + ' \u6761') : '\u5171 0 \u6761';
    $('oplog-prev').disabled = oplogOffset <= 0;
    $('oplog-next').disabled = oplogOffset + limit >= oplogTotal;
  }
  function renderOplogStats(stats) {
    $('oplog-total').textContent = stats.total || 0;
    $('oplog-retention').textContent = (stats.retention_days || 7) + ' \u5929';
    $('oplog-oldest').textContent = stats.oldest ? formatLocalTime(new Date(stats.oldest).getTime()) : '\u2014';
    $('oplog-newest').textContent = stats.newest ? formatLocalTime(new Date(stats.newest).getTime()) : '\u2014';
    const byLevel = stats.by_level || {};
    $('oplog-levels').textContent = (byLevel.info || 0) + ' / ' + (byLevel.warning || 0) + ' / ' + (byLevel.error || 0);
    const byCat = stats.by_category || {};
    $('oplog-cats').textContent = Object.keys(byCat).length ? Object.keys(byCat).sort().map(c => c + ':' + byCat[c]).join(', ') : '\u2014';
  }
  async function refreshOperationLogs() {
    if (oplogBusy) return; oplogBusy = true;
    try {
      const level = $('oplog-level').value;
      const category = $('oplog-category').value;
      const limit = parseInt($('oplog-limit').value) || 100;
      const params = new URLSearchParams({limit: String(limit), offset: String(oplogOffset)});
      if (level) params.set('level', level);
      if (category) params.set('category', category);
      const [data, stats] = await Promise.all([api('/api/operation-logs?' + params.toString()), api('/api/operation-logs/stats')]);
      renderOperationLogs(data);
      renderOplogStats(stats);
    } catch(e) { message(e.message, true); }
    finally { oplogBusy = false; }
  }
  async function cleanupOperationLogs() {
    if (oplogBusy) return;
    if (!confirm('\u7acb\u5373\u6e05\u7406\u8d85\u8fc7 7 \u5929\u7684\u64cd\u4f5c\u65e5\u5fd7\uff1f')) return;
    oplogBusy = true;
    try {
      const data = await api('/api/operation-logs/cleanup', {method: 'POST', body: '{}'});
      oplogOffset = 0;
      oplogBusy = false;
      await refreshOperationLogs();
      message('\u5df2\u6e05\u7406 ' + (data.removed || 0) + ' \u6761\u8fc7\u671f\u65e5\u5fd7\u3002');
    } catch(e) { message(e.message, true); }
    finally { oplogBusy = false; }
  }
  document.querySelectorAll('.nav button').forEach(button => {
    if (button.dataset.tab === 'operation-logs') {
      button.addEventListener('click', () => { setTimeout(refreshOperationLogs, 50); });
    }
  });
  $('oplog-refresh').addEventListener('click', refreshOperationLogs);
  $('oplog-cleanup').addEventListener('click', cleanupOperationLogs);
  $('oplog-level').addEventListener('change', () => { oplogOffset = 0; refreshOperationLogs(); });
  $('oplog-category').addEventListener('change', () => { oplogOffset = 0; refreshOperationLogs(); });
  $('oplog-limit').addEventListener('change', () => { oplogOffset = 0; refreshOperationLogs(); });
  $('oplog-prev').addEventListener('click', () => { const limit = parseInt($('oplog-limit').value) || 100; oplogOffset = Math.max(0, oplogOffset - limit); refreshOperationLogs(); });
  $('oplog-next').addEventListener('click', () => { const limit = parseInt($('oplog-limit').value) || 100; oplogOffset = Math.min(oplogTotal, oplogOffset + limit); refreshOperationLogs(); });

  refresh(); setInterval(refresh, 10000);
})();
</script>
</body>
</html>`
