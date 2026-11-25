(() => {
  const state = {
    sources: [],
    projects: [],
    results: [],
    selectedSource: '',
    selectedProject: '',
    selectedSessionId: '',
    activeDetail: null,
    searchQuery: '',
    lang: localStorage.getItem('csb_lang') || 'zh',
    theme: localStorage.getItem('csb_theme') || 'dark',
  };

  const i18n = {
    zh: {
      mcpSetup: 'MCP 通用接入',
      sourcesImport: '目录 / 跨机导入',
      sources: '配置来源',
      projects: '逻辑项目（跨系统聚合）',
      safetyNote: '纯本地只读扫描，绝不读取 Cookie / Keychain',
      allSessions: '全部本地会话',
      welcomeTitle: '跨账号、跨系统无缝接力 Claude Code 对话',
      welcomeDesc: '从左侧选择任意历史会话，可一键复制结构化交接上下文、通过本地 MCP 让新账号直接读取，或执行安全 Clean Fork 清洗克隆。',
      allSources: '全部来源',
      allProjects: '全部项目',
      turns: '轮',
      noSessions: '没有找到匹配的本地 Claude Code 会话',
    },
    en: {
      mcpSetup: 'MCP Setup',
      sourcesImport: 'Sources / Import',
      sources: 'Config Sources',
      projects: 'Logical Projects',
      safetyNote: '100% local read-only scan. Zero Cookie / Keychain access.',
      allSessions: 'All Local Sessions',
      welcomeTitle: 'Seamlessly Continue Claude Code Sessions Across Accounts & OSes',
      welcomeDesc: 'Pick any session to copy structured handoff context, connect all Claude Code clients via local MCP, or create a sanitized Clean Fork.',
      allSources: 'All Sources',
      allProjects: 'All Projects',
      turns: 'turns',
      noSessions: 'No matching local Claude Code sessions found',
    },
  };

  const $ = (id) => document.getElementById(id);
  const t = (k) => (i18n[state.lang] && i18n[state.lang][k]) || k;

  function applyThemeAndLang() {
    document.documentElement.setAttribute('data-theme', state.theme);
    $('btn-theme').textContent = state.theme === 'dark' ? '☀️' : '🌙';
    $('btn-lang').textContent = state.lang === 'zh' ? 'EN' : '中文';
    document.querySelectorAll('[data-i18n]').forEach((el) => {
      el.textContent = t(el.getAttribute('data-i18n'));
    });
  }

  let toastTimer = null;
  function showToast(msg) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.remove('hidden');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add('hidden'), 3200);
  }

  async function api(path, opts = {}) {
    const res = await fetch(path, opts);
    if (!res.ok) {
      let errMsg = `HTTP ${res.status}`;
      try {
        const j = await res.json();
        if (j.error) errMsg = j.error;
      } catch (_) {}
      throw new Error(errMsg);
    }
    return res.json();
  }

  function escapeHtml(str) {
    return String(str || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function formatRelTime(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000);
    if (diffSec < 60) return state.lang === 'zh' ? '刚刚' : 'just now';
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`;
    if (diffSec < 86400 * 30) return `${Math.floor(diffSec / 86400)}d`;
    return d.toLocaleDateString();
  }

  // Render markdown-lite with safe fenced code blocks and inline code
  function renderRichText(text) {
    const raw = String(text || '');
    const parts = raw.split(/```([\w-]*)\n?([\s\S]*?)```/g);
    let html = '';
    for (let i = 0; i < parts.length; i += 3) {
      const normal = escapeHtml(parts[i] || '').replace(/`([^`\n]+)`/g, '<code>$1</code>');
      html += normal;
      if (i + 2 < parts.length) {
        const code = escapeHtml(parts[i + 2] || '');
        html += `<pre><code>${code}</code></pre>`;
      }
    }
    return html;
  }

  async function loadHealth() {
    try {
      const h = await api('/api/health');
      $('sys-meta').textContent = `${h.os}/${h.arch} · ${h.sessionCount} 会话 · 本地离线`;
      if (h.lastScanAt) {
        $('scan-time').textContent = new Date(h.lastScanAt).toLocaleTimeString();
      }
    } catch (_) {}
  }

  async function loadSources() {
    const data = await api('/api/sources');
    state.sources = data.sources || [];
    $('source-count').textContent = state.sources.length;
    const el = $('source-list');
    el.innerHTML = '';

    const allItem = document.createElement('div');
    allItem.className = `nav-item ${state.selectedSource === '' ? 'active' : ''}`;
    allItem.innerHTML = `<span class="nav-item-main">🌐 ${t('allSources')}</span>`;
    allItem.onclick = () => {
      state.selectedSource = '';
      renderAllLists();
    };
    el.appendChild(allItem);

    state.sources.forEach((src) => {
      const item = document.createElement('div');
      item.className = `nav-item ${state.selectedSource === src.id ? 'active' : ''}`;
      const icon = src.kind === 'wsl' ? '🐧' : src.kind === 'custom' ? '📂' : '💻';
      item.innerHTML = `
        <div style="min-width:0">
          <div class="nav-item-main">${icon} ${escapeHtml(src.label)}</div>
          <div class="nav-item-sub">${escapeHtml(src.configDir)}</div>
        </div>
        <span class="badge">${src.exists ? '✓' : '—'}</span>
      `;
      item.onclick = () => {
        state.selectedSource = src.id;
        state.selectedProject = '';
        renderAllLists();
      };
      el.appendChild(item);
    });
  }

  async function loadProjects() {
    const q = state.selectedSource ? `?source=${encodeURIComponent(state.selectedSource)}` : '';
    const data = await api('/api/projects' + q);
    state.projects = data.projects || [];
    $('project-count').textContent = state.projects.length;
    const el = $('project-list');
    el.innerHTML = '';

    const allItem = document.createElement('div');
    allItem.className = `nav-item ${state.selectedProject === '' ? 'active' : ''}`;
    allItem.innerHTML = `<span class="nav-item-main">🗂️ ${t('allProjects')}</span>`;
    allItem.onclick = () => {
      state.selectedProject = '';
      loadSessions();
      renderProjectActive();
    };
    el.appendChild(allItem);

    state.projects.forEach((p) => {
      const item = document.createElement('div');
      item.className = `nav-item ${state.selectedProject === p.projectKey ? 'active' : ''}`;
      item.dataset.key = p.projectKey;
      item.innerHTML = `
        <div style="min-width:0">
          <div class="nav-item-main">📁 ${escapeHtml(p.projectName)}</div>
          ${p.gitRemote ? `<div class="nav-item-sub">${escapeHtml(p.gitRemote)}</div>` : ''}
        </div>
        <span class="badge">${p.sessionCount}</span>
      `;
      item.onclick = () => {
        state.selectedProject = p.projectKey;
        loadSessions();
        renderProjectActive();
      };
      el.appendChild(item);
    });
  }

  function renderProjectActive() {
    $('project-list').querySelectorAll('.nav-item').forEach((node) => {
      const k = node.dataset.key || '';
      node.classList.toggle('active', k === state.selectedProject);
    });
  }

  async function loadSessions() {
    const params = new URLSearchParams();
    if (state.searchQuery) params.set('q', state.searchQuery);
    if (state.selectedSource) params.set('source', state.selectedSource);
    if (state.selectedProject) params.set('project', state.selectedProject);
    const data = await api('/api/sessions?' + params.toString());
    state.results = data.results || [];
    $('session-count').textContent = state.results.length;

    const listEl = $('session-list');
    listEl.innerHTML = '';
    if (state.results.length === 0) {
      listEl.innerHTML = `<div class="empty-state">${t('noSessions')}</div>`;
      return;
    }

    state.results.forEach((hit) => {
      const s = hit.session;
      const card = document.createElement('div');
      card.className = `session-card ${state.selectedSessionId === s.id ? 'active' : ''}`;
      card.dataset.id = s.id;
      card.innerHTML = `
        <div class="session-title">${escapeHtml(s.title)}</div>
        <div class="session-snippet">${escapeHtml(hit.snippet || s.firstPrompt || '')}</div>
        <div class="session-foot">
          <span class="pill pill-proj">${escapeHtml(s.projectName)}</span>
          <span class="pill">${s.turnCount} ${t('turns')}</span>
          <span class="pill">~${Math.round((s.estimatedTokens || 0) / 100) / 10}k tok</span>
          ${s.hasThinking ? '<span class="pill pill-think">Thinking</span>' : ''}
          ${s.sourceId !== 'default' ? `<span class="pill pill-src">${escapeHtml(s.sourceLabel)}</span>` : ''}
          <span style="margin-left:auto">${formatRelTime(s.updatedAt)}</span>
        </div>
      `;
      card.onclick = () => selectSession(s.id);
      listEl.appendChild(card);
    });
  }

  async function selectSession(id) {
    state.selectedSessionId = id;
    $('session-list').querySelectorAll('.session-card').forEach((c) => {
      c.classList.toggle('active', c.dataset.id === id);
    });

    try {
      const detail = await api('/api/session?id=' + encodeURIComponent(id));
      state.activeDetail = detail;
      renderDetail(detail);
    } catch (err) {
      showToast('加载会话失败: ' + err.message);
    }
  }

  function renderDetail(detail) {
    const s = detail.summary;
    $('reader-empty').classList.add('hidden');
    $('reader-view').classList.remove('hidden');

    $('detail-title').textContent = s.title;
    $('detail-badges').innerHTML = [
      `<span class="pill pill-proj">${escapeHtml(s.projectName)}</span>`,
      `<span class="pill pill-src">${escapeHtml(s.sourceLabel)}</span>`,
      s.model ? `<span class="pill">${escapeHtml(s.model)}</span>` : '',
      s.hasThinking ? `<span class="pill pill-think">含 Thinking 签名 (克隆时自动清洗)</span>` : '',
      s.subagentCount > 0 ? `<span class="pill">${s.subagentCount} subagents</span>` : '',
    ].join('');

    $('detail-meta').textContent =
      `ID: ${s.sessionId}  |  CWD: ${s.cwd || '(unknown)'}  |  Branch: ${s.gitBranch || '-'}  |  Updated: ${new Date(s.updatedAt).toLocaleString()}`;

    const filesEl = $('detail-files');
    if (s.touchedFiles && s.touchedFiles.length > 0) {
      filesEl.classList.remove('hidden');
      filesEl.innerHTML =
        '<span>📎 涉及文件:</span>' +
        s.touchedFiles.slice(0, 12).map((f) => `<span class="file-chip">${escapeHtml(f)}</span>`).join('') +
        (s.touchedFiles.length > 12 ? `<span class="pill">+${s.touchedFiles.length - 12}</span>` : '');
    } else {
      filesEl.classList.add('hidden');
    }

    const turnList = $('turn-list');
    turnList.innerHTML = '';
    (detail.turns || []).forEach((turn) => {
      const card = document.createElement('article');
      card.className = `turn-card role-${turn.role}`;
      const roleLabel = turn.role === 'user' ? '👤 User' : '🤖 Claude';
      const ts = turn.timestamp ? new Date(turn.timestamp).toLocaleTimeString() : '';

      let blocksHtml = '';
      (turn.blocks || []).forEach((blk) => {
        if (blk.type === 'text') {
          blocksHtml += `<div class="msg-text">${renderRichText(blk.text)}</div>`;
        } else if (blk.type === 'thinking') {
          const sigNote = blk.hasSignature ? ' · 含加密 signature（Clean Fork 会自动剥离）' : '';
          blocksHtml += `
            <details class="block-accordion block-thinking">
              <summary>🧠 Thinking 思考过程${sigNote}</summary>
              <pre>${escapeHtml(blk.text)}</pre>
            </details>`;
        } else if (blk.type === 'tool_use') {
          blocksHtml += `
            <details class="block-accordion block-tool">
              <summary>🛠️ Tool Call: ${escapeHtml(blk.toolName)}</summary>
              <pre>${escapeHtml(blk.toolInput || '{}')}</pre>
            </details>`;
        } else if (blk.type === 'tool_result') {
          const cls = blk.isError ? 'block-error' : '';
          const icon = blk.isError ? '❌ Tool Error' : '📤 Tool Output';
          blocksHtml += `
            <details class="block-accordion ${cls}">
              <summary>${icon}${blk.truncatedTo ? ' (已截断预览)' : ''}</summary>
              <pre>${escapeHtml(blk.text || '(empty)')}</pre>
            </details>`;
        }
      });

      card.innerHTML = `
        <div class="turn-head">
          <div class="turn-role ${turn.role}">${roleLabel} <span class="badge">#${turn.index + 1}</span></div>
          <div class="turn-time">${escapeHtml(turn.model || '')} ${ts}</div>
        </div>
        <div class="turn-body">${blocksHtml}</div>
      `;
      turnList.appendChild(card);
    });
  }

  async function renderAllLists() {
    await loadSources();
    await loadProjects();
    await loadSessions();
    await loadHealth();
  }

  async function copyToClipboard(text) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (_) {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
      return true;
    }
  }

  // Modal helpers
  function openModal(id) { $(id).classList.remove('hidden'); }
  function closeModal(id) { $(id).classList.add('hidden'); }

  async function renderMCPModal() {
    const data = await api('/api/mcp/targets');
    const list = $('mcp-target-list');
    list.innerHTML = '';
    (data.targets || []).forEach((tgt) => {
      const card = document.createElement('div');
      card.className = 'target-card';
      const statusBadge = tgt.installed
        ? '<span class="pill" style="background:var(--green-soft);color:var(--green)">✓ 已挂载</span>'
        : '<span class="pill">未挂载</span>';
      card.innerHTML = `
        <div style="min-width:0">
          <div class="target-name">${escapeHtml(tgt.name)} ${statusBadge}</div>
          <div class="target-path">${escapeHtml(tgt.configPath)}</div>
        </div>
        <div>
          ${
            tgt.installed
              ? `<button class="btn btn-danger" data-act="uninstall" data-id="${tgt.id}">卸载</button>`
              : `<button class="btn btn-accent" data-act="install" data-id="${tgt.id}">一键挂载</button>`
          }
        </div>
      `;
      list.appendChild(card);
    });

    list.querySelectorAll('button[data-act]').forEach((btn) => {
      btn.onclick = async () => {
        const act = btn.dataset.act;
        const targetId = btn.dataset.id;
        try {
          const res = await api(`/api/mcp/${act}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ targetId }),
          });
          showToast(
            act === 'install'
              ? `✅ 已挂载 MCP！重启对应 Claude 客户端后生效${res.backupPath ? '（已自动备份原配置）' : ''}`
              : '🗑️ 已卸载 MCP 配置（已备份原配置）'
          );
          await renderMCPModal();
        } catch (err) {
          showToast('操作失败: ' + err.message);
        }
      };
    });
  }

  function renderCustomSourcesModal() {
    const list = $('custom-source-list');
    list.innerHTML = '';
    const custom = state.sources.filter((s) => s.kind === 'custom');
    if (custom.length === 0) {
      list.innerHTML = '<div style="color:var(--text-muted);font-size:12px">暂无手动添加的自定义目录（默认目录与 ~/.claude-* / WSL 已自动发现）。</div>';
      return;
    }
    custom.forEach((src) => {
      const card = document.createElement('div');
      card.className = 'target-card';
      card.innerHTML = `
        <div style="min-width:0">
          <div class="target-name">${escapeHtml(src.label)}</div>
          <div class="target-path">${escapeHtml(src.configDir)}</div>
        </div>
        <button class="btn btn-danger" data-rm="${escapeHtml(src.configDir)}">移除</button>
      `;
      list.appendChild(card);
    });
    list.querySelectorAll('button[data-rm]').forEach((btn) => {
      btn.onclick = async () => {
        await api('/api/sources?id=' + encodeURIComponent(btn.dataset.rm), { method: 'DELETE' });
        await renderAllLists();
        renderCustomSourcesModal();
        showToast('已移除来源目录');
      };
    });
  }

  // Bind UI events
  let searchTimer = null;
  $('search-input').addEventListener('input', (e) => {
    state.searchQuery = e.target.value.trim();
    $('search-clear').classList.toggle('hidden', !state.searchQuery);
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => loadSessions(), 160);
  });
  $('search-clear').onclick = () => {
    $('search-input').value = '';
    state.searchQuery = '';
    $('search-clear').classList.add('hidden');
    loadSessions();
  };

  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      $('search-input').focus();
      $('search-input').select();
    } else if (e.key === 'Escape') {
      ['modal-mcp', 'modal-fork', 'modal-sources', 'modal-desktop-push'].forEach(closeModal);
    }
  });

  document.querySelectorAll('.modal-close').forEach((btn) => {
    btn.onclick = () => closeModal(btn.dataset.close);
  });
  document.querySelectorAll('.modal-backdrop').forEach((bg) => {
    bg.addEventListener('click', (e) => {
      if (e.target === bg) bg.classList.add('hidden');
    });
  });

  $('btn-refresh').onclick = async () => {
    const res = await api('/api/refresh', { method: 'POST' });
    await renderAllLists();
    showToast(`🔄 扫描完成，共索引 ${res.sessionCount} 条本地会话`);
  };

  $('btn-lang').onclick = () => {
    state.lang = state.lang === 'zh' ? 'en' : 'zh';
    localStorage.setItem('csb_lang', state.lang);
    applyThemeAndLang();
    renderAllLists();
  };

  $('btn-theme').onclick = () => {
    state.theme = state.theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem('csb_theme', state.theme);
    applyThemeAndLang();
  };

  $('btn-mcp-modal').onclick = async () => {
    openModal('modal-mcp');
    await renderMCPModal();
  };

  $('btn-sources-modal').onclick = () => {
    openModal('modal-sources');
    $('import-result').classList.add('hidden');
    renderCustomSourcesModal();
  };

  $('btn-add-source').onclick = async () => {
    const label = $('src-label').value.trim();
    const configDir = $('src-dir').value.trim();
    if (!configDir) return showToast('请填写目录路径');
    try {
      await api('/api/sources', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label, configDir }),
      });
      $('src-label').value = '';
      $('src-dir').value = '';
      await renderAllLists();
      renderCustomSourcesModal();
      showToast('✅ 已添加自定义配置目录并完成扫描');
    } catch (err) {
      showToast('添加失败: ' + err.message);
    }
  };

  $('btn-import-bundle').onclick = async () => {
    const fileInput = $('import-file');
    if (!fileInput.files || !fileInput.files[0]) {
      return showToast('请先选择一个 .csb-bundle.zip 文件');
    }
    const fd = new FormData();
    fd.append('bundle', fileInput.files[0]);
    fd.append('targetCwd', $('import-cwd').value.trim());
    fd.append('targetConfigDir', $('import-cfg').value.trim());
    try {
      const res = await api('/api/import/bundle', { method: 'POST', body: fd });
      const box = $('import-result');
      box.classList.remove('hidden');
      box.innerHTML = `
        <strong>✅ 跨机包导入成功！</strong>（来源系统: ${escapeHtml(res.manifest?.sourceOs || 'unknown')}）
        <pre>${escapeHtml(res.result.resumeCommand)}</pre>
      `;
      await renderAllLists();
      showToast('✅ 已导入并完成路径映射');
    } catch (err) {
      showToast('导入失败: ' + err.message);
    }
  };

  $('btn-copy-handoff').onclick = async () => {
    if (!state.activeDetail) return;
    const level = $('handoff-level').value;
    const data = await api(`/api/handoff?id=${encodeURIComponent(state.activeDetail.summary.id)}&level=${level}`);
    await copyToClipboard(data.markdown);
    showToast(`📋 已复制「${level}」交接上下文，直接粘贴给任意新账号即可续聊！`);
  };

  $('btn-write-handoff').onclick = async () => {
    if (!state.activeDetail) return;
    const level = $('handoff-level').value;
    try {
      const res = await api('/api/handoff/write', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: state.activeDetail.summary.id, level }),
      });
      showToast(`📝 已写入交接文件: ${res.path}`);
    } catch (err) {
      showToast('写入失败: ' + err.message);
    }
  };

  $('btn-copy-mcp-prompt').onclick = async () => {
    if (!state.activeDetail) return;
    const level = $('handoff-level').value;
    const data = await api(`/api/handoff?id=${encodeURIComponent(state.activeDetail.summary.id)}&level=${level}`);
    await copyToClipboard(data.mcpPrompt);
    showToast('🔗 已复制 MCP 接力指令！粘贴到任意已挂载 MCP 的 Claude Code 对话即可秒读');
  };

  $('btn-open-fork').onclick = () => {
    if (!state.activeDetail) return;
    const s = state.activeDetail.summary;
    $('fork-config-dir').value = '';
    $('fork-cwd').value = s.cwd || '';
    $('fork-title').value = s.title + ' [Clean Fork]';
    $('fork-result').classList.add('hidden');
    openModal('modal-fork');
  };

  let selectedWorkspaceDir = '';
  $('btn-open-desktop-push').onclick = async () => {
    if (!state.activeDetail) return;
    const s = state.activeDetail.summary;
    $('push-title').value = s.title;
    $('push-cwd').value = s.cwd || '';
    $('desktop-push-result').classList.add('hidden');
    openModal('modal-desktop-push');

    const listEl = $('desktop-ws-list');
    listEl.innerHTML = '<div style="color:var(--text-muted);font-size:12px">正在扫描 Claude Desktop 账号目录…</div>';
    try {
      const data = await api('/api/desktop/workspaces');
      const wss = data.workspaces || [];
      listEl.innerHTML = '';
      if (wss.length === 0) {
        listEl.innerHTML = `<div style="color:var(--text-muted);font-size:12px">未检测到 Claude Desktop 本地 Code 目录（${escapeHtml(data.root || '')}）。请先在 Claude Desktop 打开一次 Code 标签页。</div>`;
        selectedWorkspaceDir = '';
        return;
      }
      selectedWorkspaceDir = wss[0].dirPath;
      wss.forEach((ws, idx) => {
        const card = document.createElement('div');
        card.className = 'target-card';
        card.style.cursor = 'pointer';
        card.style.borderColor = idx === 0 ? 'var(--accent)' : 'var(--border)';
        const badge = ws.isMostRecent
          ? '<span class="pill" style="background:var(--green-soft);color:var(--green)">★ 最近活跃（可能是当前账号）</span>'
          : '';
        const timeStr = ws.lastActivity ? new Date(ws.lastActivity).toLocaleString() : '';
        card.innerHTML = `
          <div style="min-width:0">
            <div class="target-name">账号 ${escapeHtml(ws.accountUuid.slice(0, 8))}… ${badge}</div>
            <div class="target-path">会话数: ${ws.sessionCount} · 最近: ${escapeHtml(ws.latestTitle || '(无标题)')} · ${escapeHtml(timeStr)}</div>
          </div>
          <input type="radio" name="desktop-ws" ${idx === 0 ? 'checked' : ''} />
        `;
        card.onclick = () => {
          selectedWorkspaceDir = ws.dirPath;
          listEl.querySelectorAll('.target-card').forEach((c) => (c.style.borderColor = 'var(--border)'));
          card.style.borderColor = 'var(--accent)';
          card.querySelector('input[type=radio]').checked = true;
        };
        listEl.appendChild(card);
      });
    } catch (err) {
      listEl.innerHTML = `<div style="color:var(--danger);font-size:12px">扫描失败: ${escapeHtml(err.message)}</div>`;
    }
  };

  $('btn-run-desktop-push').onclick = async () => {
    if (!state.activeDetail) return;
    if (!selectedWorkspaceDir) return showToast('请先选择一个目标账号工作区');
    try {
      const res = await api('/api/desktop/push', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: state.activeDetail.summary.id,
          workspaceDir: selectedWorkspaceDir,
          targetCwd: $('push-cwd').value.trim(),
          title: $('push-title').value.trim(),
        }),
      });
      const r = res.result;
      const box = $('desktop-push-result');
      box.classList.remove('hidden');
      box.innerHTML = `
        <strong>✅ 已推送到目标账号的 Desktop 侧边栏！</strong><br/>
        已完成签名清洗（剥离 ${r.forkResult.thinkingBlocksDropped} 个 thinking 块）。<br/>
        侧边栏索引：<code>${escapeHtml(r.sidebarIndexPath)}</code><br/>
        <strong>下一步：完全退出并重新打开 Claude Desktop，即可在 Code 左侧边栏看到该对话。</strong>
      `;
      await renderAllLists();
      showToast('📌 已推送！重启 Claude Desktop 即可在侧边栏看到');
    } catch (err) {
      showToast('推送失败: ' + err.message);
    }
  };

  $('btn-run-fork').onclick = async () => {
    if (!state.activeDetail) return;
    try {
      const res = await api('/api/clean-fork', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: state.activeDetail.summary.id,
          targetConfigDir: $('fork-config-dir').value.trim(),
          targetCwd: $('fork-cwd').value.trim(),
          newTitle: $('fork-title').value.trim(),
        }),
      });
      const box = $('fork-result');
      box.classList.remove('hidden');
      box.innerHTML = `
        <strong>✅ 干净克隆完成！</strong><br/>
        已清洗 <b>${res.thinkingBlocksDropped}</b> 个 Thinking 签名块、<b>${res.internalRecordsDropped}</b> 条服务端内部记录、<b>${res.metadataFieldsDropped}</b> 个敏感元数据字段。<br/>
        输出文件：<code>${escapeHtml(res.outputPath)}</code>
        <pre>${escapeHtml(res.resumeCommand)}</pre>
      `;
      await copyToClipboard(res.resumeCommand);
      await renderAllLists();
      showToast('🧬 克隆完成，已自动复制 claude --resume 命令！');
    } catch (err) {
      showToast('克隆失败: ' + err.message);
    }
  };

  $('btn-export-md').onclick = () => {
    if (!state.activeDetail) return;
    window.location.href = '/api/export/markdown?id=' + encodeURIComponent(state.activeDetail.summary.id);
  };

  $('btn-export-bundle').onclick = () => {
    if (!state.activeDetail) return;
    window.location.href = '/api/export/bundle?id=' + encodeURIComponent(state.activeDetail.summary.id);
  };

  // Boot
  applyThemeAndLang();
  renderAllLists().then(async () => {
    const qp = new URLSearchParams(window.location.search);
    const wantedSession = qp.get('session');
    if (wantedSession && state.results.length > 0) {
      const match = state.results.find((r) => r.session.sessionId.startsWith(wantedSession)) || state.results[0];
      if (match) await selectSession(match.session.id);
    } else if (qp.get('selectFirst') === '1' && state.results.length > 0) {
      await selectSession(state.results[0].session.id);
    }
    const modal = qp.get('modal');
    if (modal === 'desktop-push') $('btn-open-desktop-push').click();
    else if (modal === 'mcp') $('btn-mcp-modal').click();
    else if (modal === 'fork') $('btn-open-fork').click();
    else if (modal === 'sources') $('btn-sources-modal').click();
  });
})();
