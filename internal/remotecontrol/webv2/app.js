(function () {
  "use strict";
  var $ = function (id) { return document.getElementById(id); };
  var token = new URLSearchParams(location.hash.slice(1)).get("token");
  history.replaceState(null, "", location.pathname);
  var csrf = "", instance = "", epoch = "", session = "", socket = null, lastSeq = 0;
  var pending = Object.create(null), retry = 500, paired = false;

  function status(text) { $("status").textContent = text; }
  function notice(text) { $("notice").textContent = text; }
  async function post(path, body, authenticated) {
    var headers = {"Content-Type": "application/json"};
    if (authenticated) headers["X-Golder-CSRF"] = csrf;
    var response = await fetch(path, {method: "POST", headers: headers, body: JSON.stringify(body), credentials: "same-origin", cache: "no-store"});
    if (!response.ok) throw new Error("Request failed (" + response.status + ")");
    return response.json();
  }
  function requestID() {
    var data = new Uint8Array(16);
    crypto.getRandomValues(data);
    return Array.from(data, function (n) { return n.toString(16).padStart(2, "0"); }).join("");
  }
  function reconnect() {
    if (!paired) return;
    setTimeout(connect, retry);
    retry = Math.min(retry * 2, 10000);
  }
  function inputPending() {
    return Object.keys(pending).some(function (id) { return pending[id].text !== undefined; });
  }
  function send(frame, value) {
    if (!socket || socket.readyState !== WebSocket.OPEN || !session) return false;
    frame.v = 2; frame.instance_id = instance; frame.epoch = epoch;
    frame.session_id = session; frame.request_id = requestID();
    pending[frame.request_id] = value;
    socket.send(JSON.stringify(frame));
    return true;
  }
  function render(snap) {
    session = snap.session_id;
    $("identity").textContent = snap.frontend + " · " + snap.project + " · " + (snap.title || snap.session_id) + (snap.running ? " · running" : " · idle") + " · permission: " + snap.permission + (snap.truncated ? " · earlier messages omitted" : "");
    var messages = $("messages");
    messages.replaceChildren();
    (snap.messages || []).forEach(function (message) {
      var card = document.createElement("article"), label = document.createElement("strong"), text = document.createElement("pre");
      card.className = "message"; label.textContent = message.role; text.textContent = message.text;
      card.append(label, text); messages.appendChild(card);
    });
    var approvals = $("approvals");
    approvals.replaceChildren();
    (snap.approvals || []).forEach(function (approval) {
      var card = document.createElement("article"), title = document.createElement("strong"), details = document.createElement("pre");
      card.className = "approval"; title.textContent = "Approval required · " + approval.tool;
      var lines = [approval.summary, approval.justification, approval.rationale, approval.trust_dir ? "Folder: " + approval.trust_dir : "", approval.mode ? "Mode: " + approval.mode : "", approval.source || approval.level ? "Review: " + (approval.source || "") + " / " + (approval.level || "") : "", approval.origin ? "From: " + approval.origin : "", approval.tool_call_id ? "Call: " + approval.tool_call_id : "", approval.run_id ? "Run: " + approval.run_id : ""];
      (approval.needs || []).forEach(function (need) { lines.push([need.capability, need.path, need.directory, need.target].filter(Boolean).join(" · ")); });
      details.textContent = lines.filter(Boolean).join("\n");
      card.append(title, details);
      (approval.choices || []).forEach(function (choice) {
        var button = document.createElement("button");
        button.type = "button"; button.textContent = choice.label;
        button.addEventListener("click", function () {
          if (send({type: "approval.answer", approval_id: approval.id, choice_id: choice.id}, {approval: true})) {
            card.querySelectorAll("button").forEach(function (b) { b.disabled = true; });
            notice("Sending decision…");
          }
        });
        card.appendChild(button);
      });
      if (!approval.choices || approval.choices.length === 0) {
        var localOnly = document.createElement("p"); localOnly.textContent = "This approval must be completed on the Mac."; card.appendChild(localOnly);
      }
      approvals.appendChild(card);
    });
  }
  async function connect() {
    try {
      var response = await fetch("bootstrap", {credentials: "same-origin", cache: "no-store"});
      if (!response.ok) {
        if (response.status === 401 || response.status === 403 || response.status === 410) paired = false;
        throw new Error(paired ? "Connection unavailable; retrying" : "Pairing expired or was revoked");
      }
      var boot = await response.json();
      if (instance && (instance !== boot.instance_id || epoch !== boot.epoch)) lastSeq = 0;
      csrf = boot.csrf; instance = boot.instance_id; epoch = boot.epoch;
      socket = new WebSocket("wss://" + location.host + "/remote/v2/events");
      socket.onopen = function () {
        socket.send(JSON.stringify({v: 2, type: "hello", instance_id: instance, epoch: epoch, csrf: csrf, seq: lastSeq}));
        status("Synchronizing…");
      };
      socket.onmessage = function (event) {
        var frame;
        try { frame = JSON.parse(event.data); } catch (_) { socket.close(); return; }
        if (frame.v !== 2 || frame.instance_id !== instance || frame.epoch !== epoch) { socket.close(); return; }
        if (frame.type === "snapshot") {
          if (frame.snapshot.session_id !== frame.session_id || !Number.isSafeInteger(frame.seq) || frame.seq < lastSeq) { socket.close(); return; }
          lastSeq = frame.seq;
          render(frame.snapshot); paired = true; retry = 500; status("Connected");
          $("pair").hidden = true; $("session").hidden = false; $("composer").hidden = false;
          $("send").disabled = inputPending();
        } else if (frame.type === "result") {
          var request = pending[frame.request_id]; delete pending[frame.request_id];
          if (request && request.text !== undefined && frame.result === "accepted" && $("input").value === request.text) $("input").value = "";
          $("send").disabled = inputPending();
          notice(frame.result === "accepted" ? "Accepted" : "Not completed: " + frame.result + ". Check the session before retrying.");
        }
      };
      socket.onclose = function () {
        status("Disconnected"); $("send").disabled = true; session = "";
        if (Object.keys(pending).length) notice("A request outcome is unknown. Check the session before retrying.");
        pending = Object.create(null);
        reconnect();
      };
      socket.onerror = function () { socket.close(); };
    } catch (err) { status("Disconnected"); notice(err.message); reconnect(); }
  }
  $("pair-start").addEventListener("click", async function () {
    try {
      $("pair-start").disabled = true;
      var pair = await post("pair/start", {token: token, device_name: $("device").value}, false);
      token = null; $("code").textContent = pair.code; $("pair-code").hidden = false;
      $("pair-confirm").onclick = async function () {
        $("pair-confirm").disabled = true;
        for (;;) {
          try {
            var result = await post("pair/status", {pair_id: pair.pair_id, proof: pair.proof}, false);
            if (result.status === "paired") { paired = true; await connect(); return; }
            await new Promise(function (resolve) { setTimeout(resolve, 1000); });
          } catch (err) { notice(err.message + ". Start a new pairing on the Mac."); return; }
        }
      };
    } catch (err) { notice(err.message + ". Start a new pairing on the Mac."); }
  });
  $("composer").addEventListener("submit", function (event) {
    event.preventDefault();
    var text = $("input").value;
    if (!inputPending() && text.trim() && send({type: "input.submit", text: text}, {text: text})) {
      $("send").disabled = true;
      notice("Sending…");
    }
  });
  if (location.protocol !== "https:") { notice("HTTPS is required."); }
  else if (token) { $("pair").hidden = false; status("Awaiting pairing"); }
  else { paired = true; connect(); }
})();
