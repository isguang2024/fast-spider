(() => {
  const copyButton = document.querySelector("[data-copy-target]");
  if (copyButton) {
    copyButton.addEventListener("click", async () => {
      const target = document.getElementById(copyButton.dataset.copyTarget || "");
      if (!target) return;
      const value = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
        ? target.value
        : target.textContent || "";
      try {
        await navigator.clipboard.writeText(value);
        const original = copyButton.textContent;
        copyButton.textContent = "已复制";
        copyButton.disabled = true;
        window.setTimeout(() => {
          copyButton.textContent = original;
          copyButton.disabled = false;
        }, 1600);
      } catch {
        if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) {
          target.focus();
          target.select();
        }
      }
    });
  }

  for (const form of document.querySelectorAll("form[data-confirm]")) {
    form.addEventListener("submit", (event) => {
      const message = form.getAttribute("data-confirm") || "确认继续？";
      if (!window.confirm(message)) event.preventDefault();
    });
  }

  const diagnostics = document.getElementById("mcp-diagnostics");
  const diagnosticsRefresh = document.getElementById("mcp-diagnostics-refresh");
  const setText = (id, value) => {
    const target = document.getElementById(id);
    if (target) target.textContent = value || "—";
  };
  const formatTime = (value) => value ? new Date(value).toLocaleString() : "暂无事件";
  const diagnosisCopy = {
    no_initialize: ["尚无 Initialize", "客户端尚未真正连接到 MCP。"],
    initialized_no_tools_list: ["已 Initialize，尚无 Tools List", "MCP 已初始化，但客户端尚未进入工具发现。"],
    tools_listed_no_tool_call: ["已发现工具，尚无 Tool Call", "工具列表已读取，但模型尚未选择工具。"],
    tool_call_failed: ["Tool Call 失败", "调用已到达 Hub，请按错误分类检查参数、Node 或运行时。"],
    tool_call_succeeded: ["Tool Call 成功", "最近一次 MCP 工具调用已成功完成。"],
  };
  const renderDiagnostics = (data) => {
    setText("mcp-server-version", data.serverVersion);
    setText("mcp-guide-version", data.guideVersion);
    setText("mcp-last-initialize", formatTime(data.lastInitializeAt));
    setText("mcp-last-tools-list", formatTime(data.lastToolsListAt));
    setText("mcp-last-tool-call", formatTime(data.lastToolCallAt));
    setText("mcp-last-tool", data.lastToolName);
    setText("mcp-client-type", data.clientType);
    const result = data.result === "success" ? "成功" : data.result === "failure" ? `失败 · ${data.errorCode || "TOOL_ERROR"}` : "—";
    setText("mcp-result", result);
    const explanation = diagnosisCopy[data.diagnosis] || diagnosisCopy.no_initialize;
    setText("mcp-diagnosis-title", explanation[0]);
    setText("mcp-diagnosis-copy", explanation[1]);
  };
  const loadDiagnostics = async () => {
    if (!diagnostics || !diagnosticsRefresh) return;
    diagnosticsRefresh.disabled = true;
    try {
      const response = await fetch(diagnostics.dataset.diagnosticsUrl || "", { credentials: "same-origin", cache: "no-store" });
      if (!response.ok) throw new Error("request failed");
      renderDiagnostics(await response.json());
    } catch {
      setText("mcp-diagnosis-title", "诊断读取失败");
      setText("mcp-diagnosis-copy", "请确认后台登录仍有效后手动刷新。" );
    } finally {
      diagnosticsRefresh.disabled = false;
    }
  };
  if (diagnosticsRefresh) diagnosticsRefresh.addEventListener("click", loadDiagnostics);
  loadDiagnostics();
})();
