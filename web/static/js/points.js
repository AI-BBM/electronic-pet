/* M2 加分与积分流水前端组件（骨架期）。
 *
 * SPA 集成（M1 合并后）：M1 pet 视图引入本文件与 points.css，
 * 在加分入口点击时调用 Points.openAddDialog(pet)，进化触发见 Points.playLevelUp。
 * 骨架期由 web/log.html 独立页直接使用。
 */
(function () {
  'use strict';

  var PRESET_REASONS = ['作业优秀', '课堂表现', '劳动卫生', '进步之星'];

  function token() {
    // M1 登录后写入 localStorage['pet_token']；骨架期可在页面里手工粘贴
    return localStorage.getItem('pet_token') || '';
  }

  function api(path, opts) {
    opts = opts || {};
    return fetch(path, {
      method: opts.method || 'GET',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Bearer ' + token()
      },
      body: opts.body
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) {
          throw new Error(data.error || ('HTTP ' + res.status));
        }
        return data;
      });
    });
  }

  // 加分：requestId 由前端生成，双击/重试不会重复计分
  function addPoints(reason, value) {
    var requestId = 'r-' + Date.now() + '-' + Math.random().toString(36).slice(2, 10);
    return api('/api/points', {
      method: 'POST',
      body: JSON.stringify({ reason: reason, value: value, requestId: requestId })
    });
  }

  function loadLog(page) {
    return api('/api/pet/me/log?page=' + (page || 1));
  }

  // 渲染流水列表；返回 total 供分页控件使用
  function renderLog(el, data) {
    var html = data.items.map(function (item) {
      return '<li class="point-log-item">' +
        '<span class="point-log-reason">' + escapeHtml(item.reason) + '</span>' +
        '<span class="point-log-value">+' + item.value + '</span>' +
        '<time class="point-log-time">' + escapeHtml(item.createdAt) + '</time>' +
        '</li>';
    }).join('');
    el.innerHTML = html || '<li class="point-log-empty">还没有加分记录</li>';
    return data.total;
  }

  // 进度条：pet.nextLevelPoints 为 null 表示满级
  function renderProgress(el, pet) {
    var next = pet.nextLevelPoints;
    if (next == null) {
      el.innerHTML = 'Lv' + pet.level + ' · ' + pet.points + ' 分 · 已满级';
      el.style.setProperty('--progress', '100%');
      return;
    }
    var prev = pet.level === 1 ? 0 : next; // Lv1 从 0 起算；Lv2 基线为 Lv2 阈值前的累计段
    var base = pet.level === 2 ? Math.round(next / 3) : 0; // 粗略基线，SPA 接入时可换成阈值配置接口
    prev = base;
    var pct = Math.max(0, Math.min(100, Math.round(((pet.points - prev) / (next - prev)) * 100)));
    el.innerHTML = 'Lv' + pet.level + ' · ' + pet.points + ' / ' + next + ' 分';
    el.style.setProperty('--progress', pct + '%');
  }

  // 升级瞬间：光效 + 立绘切换（正式透明 PNG 由素材线提供，骨架期用 CSS 光效占位）
  function playLevelUp(container, newLevel) {
    var overlay = document.createElement('div');
    overlay.className = 'evolution-overlay';
    overlay.innerHTML = '<div class="evolution-glow"></div>' +
      '<p class="evolution-text">进化！Lv' + newLevel + '</p>';
    container.appendChild(overlay);
    setTimeout(function () { overlay.remove(); }, 2500);
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  window.Points = {
    PRESET_REASONS: PRESET_REASONS,
    addPoints: addPoints,
    loadLog: loadLog,
    renderLog: renderLog,
    renderProgress: renderProgress,
    playLevelUp: playLevelUp
  };
})();
