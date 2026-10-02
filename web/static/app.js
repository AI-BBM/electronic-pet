/* 电子宠物 M1 · 前端 SPA（原生 JS，无依赖） */
(function () {
  "use strict";

  var TOKEN_KEY = "pet_token";
  var views = ["join", "eggs", "hatch", "pet"];
  var state = { token: null, pet: null, eggs: [], selectedEgg: null, renamed: false };

  function $(sel) { return document.querySelector(sel); }
  function show(view) {
    views.forEach(function (v) {
      $('[data-view="' + v + '"]').hidden = v !== view;
    });
  }
  function saveToken(t) { localStorage.setItem(TOKEN_KEY, t); state.token = t; }
  function loadToken() { return localStorage.getItem(TOKEN_KEY); }
  function clearToken() { localStorage.removeItem(TOKEN_KEY); state.token = null; }

  function api(method, path, body) {
    var headers = { "Content-Type": "application/json" };
    if (state.token) headers["Authorization"] = "Bearer " + state.token;
    return fetch(path, {
      method: method,
      headers: headers,
      body: body ? JSON.stringify(body) : undefined,
    }).then(function (res) {
      return res.json().then(function (data) {
        return { status: res.status, data: data };
      });
    });
  }

  function showError(id, msg) {
    var el = $(id);
    if (!msg) { el.hidden = true; return; }
    el.textContent = msg;
    el.hidden = false;
  }

  var RARITY_TEXT = { common: "普通", rare: "稀有", epic: "史诗" };

  function rarityClass(r) { return "rarity-" + r; }

  /* ---------- 进入页 ---------- */
  $("#join-form").addEventListener("submit", function (e) {
    e.preventDefault();
    showError("#join-error", null);
    api("POST", "/api/join", {
      classCode: $("#join-class").value,
      name: $("#join-name").value,
      studentNo: $("#join-no").value,
    }).then(function (r) {
      if (r.status !== 200) {
        showError("#join-error", r.data.error || "进入失败，请重试");
        return;
      }
      saveToken(r.data.token);
      if (r.data.pet) {
        renderPet(r.data.pet);
        show("pet");
      } else {
        loadEggs();
        show("eggs");
      }
    });
  });

  /* ---------- 领蛋页 ---------- */
  function loadEggs() {
    api("GET", "/api/eggs").then(function (r) {
      if (r.status !== 200) {
        showError("#eggs-error", r.data.error || "蛋架加载失败");
        return;
      }
      state.eggs = r.data.eggs;
      state.selectedEgg = null;
      $("#adopt-selected").disabled = true;
      renderEggs();
    });
  }

  function renderEggs() {
    var rack = $("#egg-rack");
    rack.innerHTML = "";
    state.eggs.forEach(function (egg) {
      var el = document.createElement("div");
      el.className = "egg";
      el.style.background = "linear-gradient(160deg, " + egg.color + "cc, " + egg.color + ")";
      el.title = "神秘的蛋";
      el.addEventListener("click", function () {
        state.selectedEgg = egg.id;
        $("#adopt-selected").disabled = false;
        Array.prototype.forEach.call(rack.children, function (c) { c.classList.remove("selected"); });
        el.classList.add("selected");
      });
      rack.appendChild(el);
    });
  }

  function adopt(eggId) {
    showError("#eggs-error", null);
    api("POST", "/api/adopt", { eggId: eggId }).then(function (r) {
      if (r.status !== 200) {
        if (r.status === 409 && r.data.pet === undefined) {
          // 已有宠物：直接回档案页
          loadPet();
          show("pet");
          return;
        }
        showError("#eggs-error", r.data.error || "孵化失败，请重试");
        return;
      }
      state.pet = r.data.pet;
      playHatch(r.data.pet);
    });
  }

  $("#adopt-selected").addEventListener("click", function () { adopt(state.selectedEgg); });
  $("#adopt-random").addEventListener("click", function () { adopt("random"); });

  /* ---------- 孵化过场 ---------- */
  function playHatch(pet) {
    show("hatch");
    var eggEl = $("#hatch-egg");
    var reveal = $("#hatch-reveal");
    eggEl.classList.remove("done");
    eggEl.style.display = "";
    reveal.hidden = true;

    // 破壳动画约 2.4s（PRD：2–3 秒）后揭晓
    setTimeout(function () {
      eggEl.classList.add("done");
      $("#hatch-img").src = pet.species.imageUrl;
      $("#hatch-name").textContent = pet.name;
      var badge = $("#hatch-rarity");
      badge.textContent = RARITY_TEXT[pet.species.rarity] || pet.species.rarity;
      badge.className = "rarity-badge " + rarityClass(pet.species.rarity);
      reveal.hidden = false;
    }, 2400);
  }

  $("#go-pet").addEventListener("click", function () {
    renderPet(state.pet);
    show("pet");
  });

  /* ---------- 我的宠物 ---------- */
  function renderPet(pet) {
    state.pet = pet;
    state.renamed = false;
    $("#pet-img").src = pet.species.imageUrl;
    $("#pet-name").textContent = pet.name;
    $("#pet-species").textContent = "种类：" + pet.species.name;
    var badge = $("#pet-rarity");
    badge.textContent = RARITY_TEXT[pet.species.rarity] || pet.species.rarity;
    badge.className = "rarity-badge " + rarityClass(pet.species.rarity);
    $("#pet-level").textContent = "Lv" + pet.level;
    var next = pet.nextLevelPoints;
    if (next == null) {
      $("#pet-progress").style.width = "100%";
      $("#pet-points").textContent = "累计 " + pet.points + " 分 · 已满级";
    } else {
      var pct = next > 0 ? Math.min(100, Math.round((pet.points / next) * 100)) : 0;
      $("#pet-progress").style.width = pct + "%";
      $("#pet-points").textContent = pet.points + " / " + next + " 分";
    }
  }

  function loadPet() {
    api("GET", "/api/pet/me").then(function (r) {
      if (r.status !== 200) {
        if (r.status === 401) { clearToken(); show("join"); return; }
        if (r.status === 404) { loadEggs(); show("eggs"); return; }
        return;
      }
      renderPet(r.data.pet);
      show("pet");
    });
  }

  $("#rename-btn").addEventListener("click", function () {
    if (state.renamed) return;
    var name = prompt("给宠物取个名字吧（一生一次机会）:");
    if (name == null) return;
    api("POST", "/api/pet/name", { name: name }).then(function (r) {
      if (r.status !== 200) {
        alert(r.data.error || "改名失败");
        return;
      }
      renderPet(r.data.pet);
      state.renamed = true;
    });
  });

  /* ---------- 启动 ---------- */
  (function boot() {
    var token = loadToken();
    if (!token) { show("join"); return; }
    state.token = token;
    loadPet();
  })();
})();
