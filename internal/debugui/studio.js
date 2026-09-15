/* Plain browser JS: no build step, no external dependencies. */
(() => {
  const el = id => document.getElementById(id);
  let saved = null, controller = null, transcript = [];
  const session = Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');
  async function api(path, data, signal) {
    const response = await fetch(path, {method: data === undefined ? 'GET' : 'POST',
      headers: {'Content-Type': 'application/json'}, body: data === undefined ? undefined : JSON.stringify(data), signal});
    const result = await response.json();
    if (!response.ok || result.error) throw new Error(result.error || `HTTP ${response.status}`);
    return result;
  }
  document.querySelectorAll('[data-tab]').forEach(button => button.onclick = () => {
    document.querySelectorAll('[data-tab]').forEach(b => b.setAttribute('aria-selected', String(b === button)));
    document.querySelectorAll('[data-view]').forEach(card => card.hidden = card.dataset.view !== button.dataset.tab);
  });
  function applySettings(settings) {
    saved = settings;
    el('studioPersona').value = settings.persona;
    el('studioTurns').value = settings.history_turns;
    el('studioOutput').value = settings.output_mode;
    el('studioAddr').value = settings.vtuber_addr;
    el('studioOpen').removeAttribute('href');
    try { const u = new URL(settings.vtuber_addr); if (['http:', 'https:'].includes(u.protocol)) el('studioOpen').href = u.href; } catch (_) {}
  }
  api('/debug/studio').then(applySettings).catch(e => el('studioState').textContent = e.message);
  el('studioSave').onclick = async () => {
    el('studioSave').disabled = true;
    try {
      const settings = await api('/debug/studio', {persona: el('studioPersona').value,
        history_turns: Number(el('studioTurns').value), output_mode: el('studioOutput').value, vtuber_addr: el('studioAddr').value.trim()});
      applySettings(settings);
      el('studioState').textContent = '已保存，下一轮生效';
      loadConfig();
    } catch (e) { el('studioState').textContent = e.message; }
    finally { el('studioSave').disabled = false; }
  };
  el('studioCheck').onclick = async () => {
    el('studioCheck').disabled = true;
    el('studioHealth').textContent = '正在检测已保存的地址…';
    try {
      const s = await api('/debug/integration');
      el('studioHealth').textContent = `桥接已连接 · ${s.character || '未命名角色'} · ${s.clients} 个前端 · ${s.busy ? '播报中' : '空闲'}` +
        (s.clients ? '' : '。请打开 Live2D 页面，才能接收语音。');
    } catch (e) { el('studioHealth').textContent = e.message; }
    finally { el('studioCheck').disabled = false; }
  };
  function message(role, text, info = '') {
    el('chatFeed').querySelector('.chat-empty')?.remove();
    const node = document.createElement('div'); node.className = 'chat-msg ' + role;
    const label = document.createElement('small'); label.textContent = info || (role === 'user' ? el('chatUser').value : '主播');
    const content = document.createElement('div'); content.textContent = text;
    node.append(label, content); el('chatFeed').append(node);
    el('chatFeed').scrollTop = el('chatFeed').scrollHeight;
    transcript.push({role, text, info, at: new Date().toISOString()});
    return node;
  }
  function stopAudio() { document.querySelectorAll('audio').forEach(a => { a.pause(); a.currentTime = 0; }); }
  el('chatStop').onclick = stopAudio;
  async function send() {
    const text = el('chatText').value.trim(); if (!text || controller) return;
    controller = new AbortController();
    el('chatSend').disabled = true; el('chatClear').disabled = true; el('chatCancel').disabled = false;
    el('chatText').value = ''; message('user', text); el('chatState').textContent = '正在接话…';
    const start = performance.now();
    try {
      const data = await api('/debug/chat', {session, text, user: el('chatUser').value, mute: el('chatMute').checked}, controller.signal);
      const r = data.result;
      const duration = ((performance.now() - start) / 1000).toFixed(1);
      const node = message('assistant', r.reply_text || r.skip_reason || '这一轮没有回复', `${r.emotion || '—'} · ${duration}s`);
      const clips = [];
      for (const url of data.audio_urls || []) {
        const audio = document.createElement('audio'); audio.controls = true; audio.preload = 'none'; audio.src = url; node.append(audio); clips.push(audio);
      }
      el('chatState').textContent = r.tts_error || (r.tts_applied ? '回复已生成' : '文字回复');
      // Avoid duplicate playback when the same audio is sent to Live2D.
      if (clips.length && el('chatAuto').checked && saved?.output_mode !== 'vtuber') {
        stopAudio();
        try { await clips[0].play(); } catch (_) { el('chatState').textContent = '浏览器未允许自动播放，请点击回复中的播放按钮'; }
      }
    } catch (e) {
      const text = e.name === 'AbortError' ? '本轮已取消' : e.message;
      message('assistant', text, '状态'); el('chatState').textContent = text;
    } finally {
      controller = null; el('chatSend').disabled = false; el('chatClear').disabled = false; el('chatCancel').disabled = true;
    }
  }
  el('chatSend').onclick = send;
  el('chatText').onkeydown = e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); } };
  el('chatCancel').onclick = () => controller?.abort();
  document.querySelectorAll('[data-example]').forEach(b => b.onclick = () => { el('chatText').value = b.dataset.example; el('chatText').focus(); });
  el('chatClear').onclick = async () => {
    try { await api('/debug/chat/clear', {session}); stopAudio(); el('chatFeed').replaceChildren(); transcript = []; el('chatState').textContent = '上下文已清空'; }
    catch (e) { el('chatState').textContent = e.message; }
  };
  el('chatExport').onclick = () => {
    const url = URL.createObjectURL(new Blob([JSON.stringify(transcript, null, 2)], {type: 'application/json'}));
    const a = document.createElement('a'); a.href = url; a.download = 'mili-conversation.json'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
})();
