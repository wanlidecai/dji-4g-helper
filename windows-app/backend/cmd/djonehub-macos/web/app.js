const $ = (selector) => document.querySelector(selector);
let smsMessages = [];
let smsSelectedID = null;
let smsFilter = "all";
let smsPollInFlight = false;
let callHistoryRows = [];
let callFilter = "all";
let callStatus = null;
let callActionBusy = false;
let eventCursor = null;
let eventInstanceID = null;
let eventPollInFlight = false;
let platform = /Win/i.test(navigator.platform || navigator.userAgent) ? "windows" : "darwin";
let currentView = "network";
let latestStatus = null;
let lastMissedRead = safeStored("wlc-calls-seen") || "";

function safeStored(key) { try { return localStorage.getItem(key); } catch (_) { return null; } }
function storeValue(key, value) { try { localStorage.setItem(key, value); } catch (_) {} }
function removeStored(key) { try { localStorage.removeItem(key); } catch (_) {} }
function icon(name) { const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); const use = document.createElementNS(svg.namespaceURI, "use"); use.setAttribute("href", `#i-${name}`); svg.append(use); return svg; }
function element(tag, className, text) { const el = document.createElement(tag); if (className) el.className = className; if (text !== undefined) el.textContent = text; return el; }
function validDate(value) { const date = new Date(value); return Number.isFinite(date.getTime()) ? date : null; }
function formatDate(value, compact = false) { const date = validDate(value); if (!date) return "时间未知"; const now = new Date(); if (compact && date.toDateString() === now.toDateString()) return date.toLocaleTimeString("zh-CN", {hour:"2-digit",minute:"2-digit",hour12:false}); if (compact) return date.toLocaleDateString("zh-CN", {month:"numeric",day:"numeric"}); return date.toLocaleString("zh-CN", {year:"numeric",month:"long",day:"numeric",hour:"2-digit",minute:"2-digit",hour12:false}); }
function badge(id, count) { const el = $(id); el.textContent = count > 99 ? "99+" : String(count); el.hidden = count === 0; }

let esimHealthPollTimer = null;
let esimHealthInFlight = false;
let networkTrafficTimer = null;
let networkTrafficPrevious = null;
let networkTrafficInFlight = false;
let callPollInFlight = false;
let lastActiveCallID = null;
let cellularPolicyBusy = false;

function setThemePreference(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    storeValue("wlc-theme", theme);
    removeStored("vohive-theme");
  } else {
    delete document.documentElement.dataset.theme;
    removeStored("wlc-theme");
    removeStored("djonehub-theme");
    removeStored("vohive-theme");
  }
  document.querySelectorAll("[data-theme-option]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themeOption === theme));
  });
}

const savedTheme = safeStored("wlc-theme") || safeStored("djonehub-theme") || safeStored("vohive-theme");
setThemePreference(savedTheme === "light" || savedTheme === "dark" ? savedTheme : "auto");
document.querySelectorAll("[data-theme-option]").forEach((button) => {
  button.addEventListener("click", () => setThemePreference(button.dataset.themeOption));
});

const operatorNames = new Map([
  ["CHN-UNICOM", "中国联通"],
  ["CHINA UNICOM", "中国联通"],
  ["UNICOM", "中国联通"],
  ["46001", "中国联通"],
  ["46006", "中国联通"],
  ["46009", "中国联通"],
  ["CHINA MOBILE", "中国移动"],
  ["CMCC", "中国移动"],
  ["CHN-CMCC", "中国移动"],
  ["46000", "中国移动"],
  ["46002", "中国移动"],
  ["46004", "中国移动"],
  ["46007", "中国移动"],
  ["46008", "中国移动"],
  ["CHINA TELECOM", "中国电信"],
  ["CHN-CT", "中国电信"],
  ["CTCC", "中国电信"],
  ["46003", "中国电信"],
  ["46005", "中国电信"],
  ["46011", "中国电信"],
  ["CBN", "中国广电"],
  ["CHN-CBN", "中国广电"],
  ["CHINA BROADNET", "中国广电"],
  ["46015", "中国广电"],
]);

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function renderCellularPolicy(state) {
  const button = $("#cellular-policy-toggle");
  const forceOff = Boolean(state?.force_off);
  const enabled = !forceOff;
  button.setAttribute("aria-checked", String(enabled));
  setNetworkTrafficPolling(enabled);
  $("#policy-copy").textContent = forceOff ? "已停用蜂窝数据" : "Wi‑Fi 优先，4G 备用";
  button.title = forceOff
    ? "允许电脑使用 4G 网络"
    : "关闭后停止电脑使用 4G；短信与来电仍然提醒";
}

async function loadCellularPolicy() {
  try {
    renderCellularPolicy(await api("/api/network/cellular-policy"));
  } catch (error) {
    $("#cellular-policy-toggle").title = error.message;
  }
}

$("#cellular-policy-toggle").addEventListener("click", async () => {
  if (cellularPolicyBusy) return;
  cellularPolicyBusy = true;
  const button = $("#cellular-policy-toggle");
  button.disabled = true;
  const forceOff = button.getAttribute("aria-checked") === "true";
  try {
    const state = await api("/api/network/cellular-policy", {
      method: "POST",
      body: JSON.stringify({ force_off: forceOff }),
    });
    renderCellularPolicy(state);
    notice(forceOff ? "已强制关闭 4G，短信和来电监控保持运行" : "已恢复 Wi‑Fi 优先、4G 自动备用");
  } catch (error) {
    notice(error.message);
    await loadCellularPolicy();
  } finally {
    button.disabled = false;
    cellularPolicyBusy = false;
  }
});

function notice(message) {
  const el = $("#notice");
  el.textContent = message;
  el.classList.add("show");
  clearTimeout(notice.timer);
  notice.timer = setTimeout(() => el.classList.remove("show"), 2600);
}

let modalResolve = null;

function closeModal(result = null) {
  const modal = $("#app-modal");
  modal.hidden = true;
  document.body.classList.remove("modal-open");
  if (modalResolve) {
    const resolve = modalResolve;
    modalResolve = null;
    resolve(result);
  }
}

function showModal({ title, message = "", fields = [], confirmLabel = "确定", danger = false }) {
  if (modalResolve) closeModal(null);
  const modal = $("#app-modal");
  const messageElement = $("#modal-message");
  const fieldsElement = $("#modal-fields");
  const confirmButton = $("#modal-confirm");
  $("#modal-title").textContent = title;
  messageElement.textContent = message;
  messageElement.hidden = !message;
  fieldsElement.replaceChildren(...fields.map((field) => {
    const label = document.createElement("label");
    label.className = "modal-field";
    const caption = document.createElement("span");
    caption.textContent = field.label;
    const input = document.createElement("input");
    input.name = field.name;
    input.value = field.value || "";
    input.placeholder = field.placeholder || "";
    input.autocomplete = "off";
    if (field.required) input.required = true;
    label.append(caption, input);
    return label;
  }));
  confirmButton.textContent = confirmLabel;
  confirmButton.className = danger ? "danger modal-danger" : "";
  modal.hidden = false;
  document.body.classList.add("modal-open");
  const firstInput = fieldsElement.querySelector("input");
  setTimeout(() => (firstInput || confirmButton).focus(), 0);
  return new Promise((resolve) => { modalResolve = resolve; });
}

$("#modal-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const values = {};
  event.currentTarget.querySelectorAll(".modal-fields input").forEach((input) => {
    values[input.name] = input.value.trim();
  });
  closeModal(values);
});
$("#modal-cancel").addEventListener("click", () => closeModal(null));
$("#modal-close").addEventListener("click", () => closeModal(null));
$("#app-modal").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeModal(null);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#app-modal").hidden) closeModal(null);
});

async function copySMSCode(code) {
  try {
    await navigator.clipboard.writeText(code);
    notice(`验证码 ${code} 已复制`);
  } catch (error) {
    notice("复制失败，请手动复制验证码");
  }
}

function renderHardwareDetails(status) {
  const panel = $("#hardware-details");
  const device = status.usb_device;
  if (!device && !status.discovery_error) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }

  const title = document.createElement("strong");
  title.textContent = device ? "已检测到大疆 USB 设备" : "未检测到可用硬件";

  const detail = document.createElement("p");
  if (device) {
    const interfaceText = Array.isArray(device.interfaces)
      ? `${device.interfaces.length} 个 USB 接口`
      : "接口信息待读取";
    detail.textContent = [
      `${device.vendor || "DJI"} ${device.product || ""}`.trim(),
      `${device.vendor_id}:${device.product_id}`,
      device.mode,
      interfaceText,
    ].filter(Boolean).join(" · ");
  } else {
    detail.textContent = status.discovery_error || "设备未枚举";
  }

  const hint = document.createElement("small");
  hint.textContent = status.discovery_error
    ? `当前限制：${status.discovery_error}`
    : "设备连接就绪后，短信与卡片功能会自动启用。";

  panel.hidden = false;
  panel.replaceChildren(title, detail, hint);
}

function setValue(id, text, tone = "") {
  const el = $(id);
  el.textContent = text || "--";
  el.className = tone;
}

function displayOperatorName(value) {
  const raw = String(value || "").trim();
  if (!raw) return "--";
  return operatorNames.get(raw.toUpperCase()) || raw;
}

function displayWorkMode(value) {
	if (value === null || value === undefined || value === "") {
	  return { label: "待读取", tone: "muted" };
	}
  switch (Number(value)) {
    case 0: return { label: "仅短信模式", tone: "muted" };
    case 1: return { label: platform === "windows" ? "需要切换上网模式" : "4G 上网", tone: platform === "windows" ? "warn" : "info" };
    case 2: return { label: "实验模式 2", tone: "warn" };
    case 3: return { label: platform === "windows" ? "4G 上网" : "需要切换上网模式", tone: platform === "windows" ? "info" : "warn" };
    default: return { label: "待读取", tone: "muted" };
  }
}

function signalTone(dbm) {
  const value = Number(dbm);
  if (!Number.isFinite(value) || value === 0) return "muted";
  if (value >= -65) return "good";
  if (value >= -75) return "signal-fair";
  if (value >= -85) return "warn";
  if (value >= -95) return "orange";
  return "bad";
}

async function loadStatus() {
  try {
    const status = await api("/api/status");
    latestStatus = status;
    const carrier = displayOperatorName(status.operator);
    setValue("#operator", carrier, status.operator ? "info" : "muted");
    setValue("#signal", status.signal_dbm ? `${status.signal_dbm} dBm` : "--", signalTone(status.signal_dbm));
    setValue("#network-mode", status.network_mode || status.reg_status_text || "--", status.network_mode ? "info" : "muted");
    setValue("#sim", status.sim_inserted ? "已插入" : (status.usb_device ? "待读取" : "未检测到"), status.sim_inserted ? "good" : "warn");
    const workMode = displayWorkMode(status.usbnet_mode);
    setValue("#work-mode", workMode.label, workMode.tone);
    const connected = Boolean(status.sim_inserted || status.operator || status.usb_device);
    $("#device-connection").textContent = connected ? "模块已连接" : "等待连接模块";
    $("#device-summary").textContent = status.operator ? `${carrier} · ${status.network_mode || "蜂窝网络"}` : status.hardware_status || "请连接大疆 4G 模块";
    $("#header-carrier").textContent = status.operator ? `${carrier} ${status.network_mode || ""}`.trim() : connected ? "模块已连接" : "等待连接";
    document.querySelectorAll(".connection-dot").forEach(el => { el.classList.toggle("offline", !connected); el.classList.remove("connecting"); });
    renderHardwareDetails(status);
  } catch (error) {
    $("#device-connection").textContent = "连接暂时中断";
    $("#device-summary").textContent = "正在等待模块恢复";
    $("#device-summary").title = error.message;
    $("#header-carrier").textContent = "重新连接中";
    document.querySelectorAll(".connection-dot").forEach(el => el.classList.add("offline"));
  }
}

function messageID(message) { return message.id || [message.sender, message.timestamp, message.content].join("\u0000"); }
function messageUnread(message) { return message.read === false; }
function renderSMS() {
  const list = $("#sms-list");
  const search = $("#sms-search").value.trim().toLocaleLowerCase();
  const unread = smsMessages.filter(messageUnread).length;
  $("#sms-count").textContent = smsMessages.length;
  $("#sms-unread-count").textContent = unread;
  badge("#sms-badge", unread);
  $("#mark-sms-read").disabled = unread === 0;
  $("#sms-filter-all").classList.toggle("selected", smsFilter === "all");
  $("#sms-filter-unread").classList.toggle("selected", smsFilter === "unread");
  const rows = smsMessages.filter(message => (smsFilter !== "unread" || messageUnread(message)) && (!search || `${message.sender || ""} ${message.content || ""}`.toLocaleLowerCase().includes(search)));
  if (!rows.length) {
    list.className = "message-list empty";
    list.textContent = search ? "没有找到相关短信" : smsFilter === "unread" ? "所有消息都已读过" : "暂时没有短信，收到后会显示在这里";
    return;
  }
  list.className = "message-list";
  list.replaceChildren(...rows.map(message => {
    const row = element("button", `message-row${smsSelectedID === messageID(message) ? " selected" : ""}${messageUnread(message) ? " unread" : ""}`);
    row.type = "button";
    row.setAttribute("aria-label", `${messageUnread(message) ? "未读短信，" : ""}${message.sender || "未知号码"}，${message.content || ""}`);
    row.setAttribute("aria-pressed", String(smsSelectedID === messageID(message)));
    const avatar = element("span", "message-avatar", (message.sender || "?").slice(-2));
    const body = element("span", "message-row-body");
    const top = element("span", "message-row-top");
    top.append(element("span", "message-sender", message.sender || "未知号码"), element("time", "", formatDate(message.timestamp, true)));
    if (messageUnread(message)) top.append(element("span", "unread-dot"));
    body.append(top, element("span", "message-preview", `${message.direction === "outgoing" ? "我：" : ""}${message.content || "（空短信）"}`));
    if (message.code) body.append(element("span", "message-row-code", message.code));
    row.append(avatar, body);
    row.addEventListener("click", () => selectSMS(message));
    return row;
  }));
}

function renderSMSDetail(message) {
  const panel = $("#sms-detail");
  const head = element("div", "message-detail-head");
  const back = element("button", "mobile-detail-back"); back.type = "button"; back.setAttribute("aria-label", "返回短信列表"); back.append(icon("back")); back.addEventListener("click", () => $(".messages-panel").classList.remove("detail-open"));
  head.append(back, element("span", "message-avatar", (message.sender || "?").slice(-2)));
  const info = element("div", ""); info.append(element("h2", "", message.sender || "未知号码"), element("p", "", message.direction === "outgoing" ? "发出的短信" : "收到的短信")); head.append(info);
  const actions = element("div", "detail-actions");
  const reply = element("button", "secondary", message.direction === "outgoing" ? "再发一条" : "回复"); reply.type = "button"; reply.prepend(icon("message")); reply.addEventListener("click", () => openCompose(message.sender || ""));
  const call = element("button", "secondary", "拨号"); call.type = "button"; call.prepend(icon("phone")); call.addEventListener("click", () => { showView("calls"); $("#call-number").value = message.sender || ""; $("#call-number").focus(); });
  actions.append(reply, call); head.append(actions);
  const body = element("div", "message-detail-body");
  body.append(element("div", "message-date", formatDate(message.timestamp)), element("div", `message-bubble${message.direction === "outgoing" ? " outgoing" : ""}`, message.content || "（空短信）"));
  if (message.code) {
    const verification = element("div", "verification-card");
    const info = element("div", ""); info.append(element("small", "", "识别到验证码"), element("strong", "", message.code));
    const copy = element("button", "secondary", "复制验证码"); copy.type = "button"; copy.addEventListener("click", () => copySMSCode(message.code)); verification.append(info, copy); body.append(verification);
  }
  const footer = element("div", "message-detail-foot", message.direction === "outgoing" ? "通过当前 SIM 发出 · 已保存在本机" : "通过当前 SIM 接收 · 已保存在本机"); footer.prepend(icon("message"));
  panel.replaceChildren(head, body, footer);
}

async function markSMSRead(messages) {
  const ids = messages.filter(messageUnread).map(message => message.id).filter(Boolean);
  if (!ids.length) return;
  await api("/api/sms/read", {method:"POST",body:JSON.stringify({ids})});
  messages.forEach(message => { if (ids.includes(message.id)) message.read = true; });
  renderSMS();
}
async function selectSMS(message) {
  smsSelectedID = messageID(message); renderSMSDetail(message); renderSMS();
  $(".messages-panel").classList.add("detail-open");
  try { await markSMSRead([message]); } catch (error) { notice(`无法标记已读：${error.message}`); }
}
async function loadSMS() {
  if (smsPollInFlight) return;
  smsPollInFlight = true;
  try {
    const [messages, status] = await Promise.all([api("/api/sms"), api("/api/sms/status")]);
    smsMessages = (Array.isArray(messages) ? messages : []).sort((a,b) => (validDate(b.timestamp)?.getTime() || 0) - (validDate(a.timestamp)?.getTime() || 0));
    $("#sms-status").textContent = status.last_poll_error ? "短信暂时无法更新，将自动重试" : status.polling ? "收件箱自动更新 · 新消息会提醒你" : "收件箱已更新";
    $("#sms-status").title = status.last_poll_error || "";
    renderSMS();
    const selected = smsMessages.find(message => messageID(message) === smsSelectedID);
    if (selected) renderSMSDetail(selected);
  } catch (error) { $("#sms-status").textContent = "暂时无法读取，正在等待模块恢复"; $("#sms-status").title = error.message; }
  finally { smsPollInFlight = false; }
}

function callStateLabel(call) {
  switch (call?.state) {case "incoming":return "收到来电";case "waiting":return "来电等待";case "active":return "通话已接通";case "dialing":return "正在拨号";case "alerting":return "等待对方接听";case "held":return "通话保持中";default:return "通话处理中";}
}
function callDuration(call) { const start = validDate(call.started_at); const end = validDate(call.ended_at); if (!start || !end || call.missed || !call.answered) return ""; const seconds = Math.max(0,Math.round((end-start)/1000)); return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds/60)} 分 ${seconds%60} 秒`; }
function markCallsSeen() { lastMissedRead = new Date().toISOString(); storeValue("wlc-calls-seen",lastMissedRead); badge("#calls-badge",0); }
function renderCallHistory(history) {
  callHistoryRows = Array.isArray(history) ? history : [];
  const list = $("#call-history"); const rows = callHistoryRows.filter(call => callFilter !== "missed" || call.missed);
  $("#call-history-count").textContent = `${callHistoryRows.length} 条`;
  $("#call-filter-all").classList.toggle("selected",callFilter === "all"); $("#call-filter-missed").classList.toggle("selected",callFilter === "missed");
  if (currentView === "calls") markCallsSeen(); else badge("#calls-badge",callHistoryRows.filter(call => call.missed && (validDate(call.ended_at || call.started_at)?.getTime() || 0) > (validDate(lastMissedRead)?.getTime() || 0)).length);
  if (!rows.length) {list.className="list empty";list.textContent=callFilter === "missed"?"没有未接来电":"还没有通话记录";return;}
  list.className="list";list.replaceChildren(...rows.map(call => {
    const row=element("article",`call-history-item${call.missed?" missed":""}`); const avatar=element("span","call-history-icon");avatar.append(icon("phone"));
    const body=element("div","call-history-body");body.append(element("strong","",call.number||"未知号码"));
    const label=call.missed?"未接来电":call.rejected?"已拒接":call.direction === "outgoing"?"已拨电话":call.answered?"已接来电":"来电结束";
    body.append(element("p","",[label].filter(Boolean).join(" · ")));
    row.append(avatar,body,element("time","",formatDate(call.started_at,true)));
    if(call.number){const dial=element("button","text-button icon-button");dial.type="button";dial.setAttribute("aria-label",`回拨 ${call.number}`);dial.append(icon("phone"));dial.addEventListener("click",()=>{$("#call-number").value=call.number;$("#call-number").focus();});row.append(dial);}return row;
  }));
}
function renderCallCapabilities(status) {
  const capabilities=status.capabilities||{}; const ready=Boolean(capabilities.call_control_supported);
  $("#dial-call").disabled = callActionBusy || !capabilities.dial_available || Boolean(status.active);
  $("#call-capability-title").textContent = !capabilities.probe_complete ? "正在确认模块通话能力" : !ready ? "当前模块未确认通话控制能力" : capabilities.audio_available ? "通话功能已就绪" : "通话控制可用，语音通道未就绪";
  $("#call-capability-detail").textContent = capabilities.audio_note || "电脑尚未确认可用的通话音频通道，无法通过电脑麦克风与扬声器交谈。";
  $("#dial-status").textContent = status.active ? "正在处理当前通话" : !capabilities.probe_complete ? "正在读取模块能力…" : capabilities.dial_available ? capabilities.audio_available ? "可以拨打电话" : "可控制拨号 · 电脑通话声音未连接" : "当前暂不支持拨号";
}
async function loadCalls() {
  if(callPollInFlight)return;callPollInFlight=true;
  try{
    const status=await api("/api/calls/status");callStatus=status;const active=status.active;const incoming=active&&["incoming","waiting"].includes(active.state); const capability=status.capabilities||{};
    $("#call-monitor-status").textContent=status.last_poll_error?"暂时无法读取来电，正在等待模块恢复":status.polling?"来电提醒在后台持续运行":"通话状态已更新";
    $("#call-monitor-status").title=status.last_poll_error||"";
    $("#active-call").hidden=!active;$("#incoming-banner").hidden=!active || currentView === "calls";
    if(active){
      $("#active-call-label").textContent=callStateLabel(active);$("#active-call-number").textContent=active.number||"未知号码";$("#active-call-time").textContent=formatDate(active.started_at);
      $("#incoming-banner-label").textContent=callStateLabel(active);$("#incoming-banner-number").textContent=active.number||"未知号码";
      $("#answer-call").hidden=!incoming;$("#banner-answer").hidden=!incoming;$("#answer-call").disabled=$("#banner-answer").disabled=callActionBusy||!capability.answer_available;
      $("#hangup-call").textContent=$("#banner-hangup").textContent=incoming?"拒接":"挂断";$("#hangup-call").disabled=$("#banner-hangup").disabled=callActionBusy||!capability.hangup_available;
      lastActiveCallID=active.id;
    }else{lastActiveCallID=null;}
    renderCallCapabilities(status);renderCallHistory(status.history);
  }catch(error){$("#call-monitor-status").textContent="来电监听暂时中断";$("#call-monitor-status").title=error.message;document.querySelectorAll("#dial-call,#answer-call,#hangup-call,#banner-answer,#banner-hangup").forEach(button=>button.disabled=true);}
  finally{callPollInFlight=false;}
}
async function performCallAction(action) {
  if(callActionBusy)return;
  const number=$("#call-number").value.trim();
  const callID=callStatus?.active?.id;
  if(action === "dial"&&!/^\+?[0-9*#]{1,24}$/.test(number)){notice("请输入有效的电话号码");$("#call-number").focus();return;}
  if(["dial","answer"].includes(action)&&!callStatus?.capabilities?.audio_available){
    const confirmed=await showModal({title:action==="dial"?"拨打电话":"接听电话",message:"当前模块可以控制通话，但电脑尚未确认通话音频通道。继续后，无法通过电脑麦克风与扬声器交谈。",confirmLabel:action==="dial"?"继续拨号":"继续接听"});if(!confirmed)return;
  }
  callActionBusy=true;document.querySelectorAll("#dial-call,#answer-call,#hangup-call,#banner-answer,#banner-hangup").forEach(button=>button.disabled=true);
  try{await api(`/api/calls/${action}`,{method:"POST",body:JSON.stringify(action==="dial"?{number}:{call_id:callID})});notice(action==="dial"?"拨号请求已发送":action==="answer"?"接听请求已发送":"通话已结束");}
  catch(error){notice(`操作失败：${error.message}`);}finally{callActionBusy=false;await loadCalls();}
}

async function loadEvents() {
  if(eventPollInFlight)return;eventPollInFlight=true;
  try{
    const query=eventCursor===null?"":`?after=${eventCursor}&instance_id=${encodeURIComponent(eventInstanceID||"")}`;
    const result=await api(`/api/events${query}`);eventCursor=result.cursor;eventInstanceID=result.instance_id;
    (result.events||[]).forEach(event=>{if(event.type==="sms")void loadSMS();else void loadCalls();notice(event.type==="sms"?`新短信：${event.number||"未知号码"}`:event.type==="missed_call"?`未接来电：${event.number||"未知号码"}`:`来电：${event.number||"未知号码"}`);});
  }catch(_){/* The native host also monitors events; retry after reconnect. */}finally{eventPollInFlight=false;}
}

function profileRows(value) {
  const groups = Array.isArray(value) ? value : value?.profiles || [];
  return groups.flatMap((group) =>
    (group.profiles || []).map((profile) => ({ ...profile, aid: group.aid_hex || "" })),
  );
}

function profileDisplayName(profile) {
  return profile?.name || profile?.service_provider_name || profile?.iccid || "未命名 Profile";
}

function activeProfile(profiles) {
  return profiles.find((profile) => profile.state === 1) || null;
}

function maskIdentifier(value, keep = 4) {
  const text = String(value || "");
  if (text.length <= keep * 2) return text;
  return `${text.slice(0, keep)} ${"•".repeat(Math.max(4, text.length - keep * 2))} ${text.slice(-keep)}`;
}

function maskPhoneNumber(value) {
  const text = String(value || "").trim();
  const digitCount = [...text].filter((char) => /\d/.test(char)).length;
  if (digitCount <= 8) return text;
  let digitIndex = 0;
  return [...text].map((char) => {
    if (!/\d/.test(char)) return char;
    digitIndex += 1;
    return digitIndex > 4 && digitIndex <= digitCount - 4 ? "*" : char;
  }).join("");
}

async function copyIdentifier(value, label) {
  try {
    await navigator.clipboard.writeText(value);
    notice(`${label} 已复制`);
  } catch (error) {
    notice(`复制 ${label} 失败，请手动复制`);
  }
}

async function editProfileNote(profile, note) {
  const values = await showModal({
    title: "编辑模块资料",
    message: "这些资料保存在大疆模块中，并按 ICCID 与当前 Profile 关联。",
    confirmLabel: "保存",
    fields: [
      { name: "label", label: "模块内名称", value: note.label || "", placeholder: "可选" },
      { name: "phone", label: "模块号码", value: note.phone || "", placeholder: "可选" },
      { name: "tags", label: "用途标签", value: note.tags || "", placeholder: "例如：英国验证码" },
    ],
  });
  if (!values) return;
  try {
    await api("/api/esim/module-notes", {
      method: "PUT",
      body: JSON.stringify({ iccid: profile.iccid, label: values.label, phone: values.phone, tags: values.tags }),
    });
    notice("模块资料已保存");
    await loadESIM();
  } catch (error) {
    notice(error.message);
  }
}

function phonebookCheck(label, ok, detail) {
  const card = document.createElement("div");
  card.className = `phonebook-check ${ok ? "ok" : ""}`;
  const title = document.createElement("strong");
  title.textContent = label;
  const text = document.createElement("small");
  text.textContent = detail;
  card.append(title, text);
  return card;
}

async function probeESIMPhonebook() {
  const button = $("#probe-esim-phonebook");
  const status = $("#esim-phonebook-status");
  const resultPanel = $("#esim-phonebook-result");
  button.disabled = true;
  status.textContent = "正在检测卡内通讯录能力，不会写入联系人...";
  resultPanel.hidden = true;
  try {
    const result = await api("/api/esim/phonebook/probe", { method: "POST" });
    const supported = result.storage_supported && result.storage_selected;
    const portable = supported && result.read_supported && result.write_supported;
    status.textContent = portable
      ? "已确认当前 Profile 支持卡内通讯录读写；尚未写入任何联系人。"
      : "当前 Profile 未完整确认卡内通讯录读写能力；不会进行写入。";
    resultPanel.replaceChildren(
      phonebookCheck("SIM 通讯录", result.storage_supported, result.storage_supported ? "支持 SM 卡内存储" : "未发现 SM 卡内存储"),
      phonebookCheck("当前卡片", result.storage_selected, result.storage_selected ? "已安全选中 SM 存储" : "无法选中 SM 存储"),
      phonebookCheck("读取能力", result.read_supported, result.read_supported ? "模块支持读取卡内联系人" : "模块未确认读取命令"),
      phonebookCheck("写入接口", result.write_supported, result.write_supported ? "模块声明支持写入接口" : "模块未确认写入命令"),
      phonebookCheck("当前状态", supported, result.storage_status || "未返回容量信息"),
    );
    resultPanel.hidden = false;
  } catch (error) {
    status.textContent = `通讯录检测失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
}

function esimEIDRows(value) {
  const eids = value?.chip_info?.eids;
  return Array.isArray(eids) ? eids : [];
}

function renderESIMChip(overview) {
  const panel = $("#esim-chip");
  const chip = overview?.chip_info || {};
  const eids = esimEIDRows(overview);
  if (!chip.sku_name && !chip.serial_number && !chip.firmware && !eids.length) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }
  panel.hidden = false;
  panel.replaceChildren(
    diagnosticCard("卡类型", chip.sku_name || "eUICC/eSIM 卡片"),
    diagnosticCard("固件", chip.firmware || "--", chip.serial_number ? `序列号 ${chip.serial_number}` : ""),
    diagnosticCard("EID", eids.map((item) => item.eid).filter(Boolean).join(" · ") || "--"),
  );
}

function renderESIMEIDList(overview) {
  const eids = esimEIDRows(overview);
  if (!eids.length) return [];
  return eids.map((item) => {
    const row = document.createElement("article");
    row.className = "item esim-info-row";
    const name = document.createElement("strong");
    name.textContent = "已识别 eUICC";
    const detail = document.createElement("p");
    detail.textContent = [
      item.eid ? `EID ${item.eid}` : "",
      item.aid ? `AID ${item.aid}` : "",
      item.free_nvram ? `可用空间 ${item.free_nvram}` : "",
      item.firmware ? `固件 ${item.firmware}` : "",
    ].filter(Boolean).join("\n");
    const status = document.createElement("small");
    status.textContent = item.spec || item.spec_guess || "eSIM";
    row.append(name, detail, status);
    return row;
  });
}

function renderESIMEIDPanel(rows) {
  if (!rows.length) return null;
  const panel = document.createElement("details");
  panel.className = "esim-euicc-panel";
  const heading = document.createElement("summary");
  heading.className = "esim-euicc-heading";
  const title = document.createElement("strong");
  title.textContent = "已识别 eUICC";
  const hint = document.createElement("small");
  hint.textContent = rows.length > 1 ? `${rows.length} 张 eSIM 卡片` : "卡片信息";
  heading.append(title, hint);
  panel.append(heading, ...rows);
  return panel;
}

async function loadESIMHealth() {
  if (esimHealthInFlight) return;
  esimHealthInFlight = true;
  const section = $("#esim-runtime-section");
  const panel = $("#esim-runtime");
  section.hidden = false;
  panel.replaceChildren(diagnosticCard("Profile 检查", "正在检测"));
  try {
    const health = await api("/api/esim/health");
    if (health.card_type === "physical_sim") {
      section.hidden = true;
      return;
    }
    if (!health.active_profile) {
      panel.replaceChildren(diagnosticCard("Profile 检查", health.message || "未发现已启用 Profile"));
      return;
    }
    const profile = health.active_profile;
    const signal = Number.isFinite(health.signal_dbm) ? `${health.signal_dbm} dBm` : "--";
    panel.replaceChildren(
      diagnosticCard("当前启用", profileDisplayName(profile), profile.iccid ? `ICCID ${maskIdentifier(profile.iccid)}` : ""),
      diagnosticCard("模块实际卡", health.module_iccid ? maskIdentifier(health.module_iccid) : "--", health.imsi ? `IMSI ${health.imsi}` : ""),
      diagnosticCard("蜂窝注册", health.registration || "未注册", [displayOperatorName(health.operator), health.network_mode].filter(Boolean).join(" · ")),
      diagnosticCard("信号", signal, health.registered ? "模块已接管当前 Profile" : "等待网络注册"),
    );
  } catch (error) {
    panel.replaceChildren(diagnosticCard("Profile 检查", "暂时无法读取", error.message));
  } finally {
    esimHealthInFlight = false;
  }
}

function setESIMHealthPolling(enabled) {
  clearInterval(esimHealthPollTimer);
  esimHealthPollTimer = null;
  if (!enabled) return;
  esimHealthPollTimer = setInterval(() => {
    if (currentView === "settings" && $("#esim-settings").open) void loadESIMHealth();
  }, 30000);
}

function diagnosticCard(label, value, detail = "") {
  const card = document.createElement("div");
  card.className = "diagnostic-card";
  const span = document.createElement("span");
  span.textContent = label;
  const strong = document.createElement("strong");
  strong.textContent = value || "--";
  card.append(span, strong);
  if (detail) {
    const small = document.createElement("small");
    small.textContent = detail;
    card.append(small);
  }
  return card;
}

function renderNetworkCheck(label, result) {
  const list = $("#network-checks");
  list.className = "list";
  const row = document.createElement("article");
  row.className = `item check-item ${result.ok ? "ok" : "bad"}`;
  const name = document.createElement("strong");
  name.textContent = label;
  const detail = document.createElement("p");
  detail.textContent = result.detail || result.summary || "";
  const status = document.createElement("small");
  status.textContent = result.ok ? "通过" : "未通过";
  row.append(name, detail, status);
  const existing = [...list.querySelectorAll(".item")].filter((item) => item.dataset.label !== label);
  row.dataset.label = label;
  list.replaceChildren(row, ...existing);
}

async function runNetworkCheck(label, path, button) {
  button.disabled = true;
  try {
    const result = await api(path, { method: "POST" });
    renderNetworkCheck(label, result);
    notice(result.summary || "检测完成");
  } catch (error) {
    renderNetworkCheck(label, { ok: false, summary: "检测失败", detail: error.message });
    notice(error.message);
  } finally {
    button.disabled = false;
  }
}

async function loadNetwork() {
  const grid = $("#network-grid");
  const ifaceList = $("#network-interfaces");
  $("#network-status").textContent = "正在读取网络诊断...";
  try {
    const diag = await api("/api/network");
    const active = Array.isArray(diag.active_contexts) ? diag.active_contexts.join(", ") : "";
    const apns = Array.isArray(diag.pdp_contexts)
      ? diag.pdp_contexts.map((ctx) => `${ctx.id}:${ctx.apn}`).join(" · ")
      : "";
    const addresses = Array.isArray(diag.pdp_addresses) ? diag.pdp_addresses.join(" · ") : "";
    const usb = diag.usb_device
      ? `${diag.usb_device.vendor || ""} ${diag.usb_device.product || ""} (${diag.usb_device.vendor_id}:${diag.usb_device.product_id})`
      : "未检测到";
    const route = diag.default_route || {};
    const routeText = route.interface
      ? `${route.interface}${route.gateway ? ` -> ${route.gateway}` : ""}`
      : "未知";
    grid.replaceChildren(
      diagnosticCard("USB 网卡", diag.usb_network_present ? "已识别" : "未识别", "电脑是否识别到大疆网络接口"),
      diagnosticCard("默认出口", routeText, "电脑当前使用的网络接口与网关"),
      diagnosticCard("工作模式", displayWorkMode(diag.usbnet_mode).label, "模块当前网络工作模式"),
      diagnosticCard("蜂窝数据", active ? `已激活 ${active}` : "未激活", "蜂窝数据连接状态"),
      diagnosticCard("蜂窝 IP", addresses || "无", "模块侧拿到的数据网络地址"),
      diagnosticCard("APN", apns || "无", "运营商数据配置"),
      diagnosticCard("USB 枚举", usb, diag.usb_device?.mode || ""),
    );

    const errorText = diag.errors ? ` · 错误：${Object.values(diag.errors).join("；")}` : "";
    $("#network-status").textContent = diag.usb_network_present
      ? `电脑已识别大疆网络接口${errorText}`
      : `电脑尚未识别大疆网络接口${errorText}`;

    const interfaces = Array.isArray(diag.mac_interfaces) ? diag.mac_interfaces : [];
    if (!interfaces.length) {
      ifaceList.className = "list empty";
      ifaceList.textContent = "未读取到网络接口";
      return;
    }
    ifaceList.className = "list";
    ifaceList.replaceChildren(...interfaces.map((item) => {
      const row = document.createElement("article");
      row.className = "item";
      const name = document.createElement("strong");
      name.textContent = item.name;
      const detail = document.createElement("p");
      detail.textContent = [item.kind, item.status, item.ipv4].filter(Boolean).join(" · ");
      const status = document.createElement("small");
      status.textContent = item.status === "active" ? "已连接" : "未连接";
      row.append(name, detail, status);
      return row;
    }));
  } catch (error) {
    $("#network-status").textContent = `读取网络诊断失败：${error.message}`;
    grid.replaceChildren();
    ifaceList.className = "list empty";
    ifaceList.textContent = "读取失败";
    notice(error.message);
  }
}

function formatTrafficBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  const units = ["B", "KB", "MB", "GB", "TB"];
  let amount = bytes;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : (amount >= 100 ? 0 : amount >= 10 ? 1 : 2);
  return `${amount.toFixed(digits)} ${units[unit]}`;
}

async function loadNetworkTraffic() {
  if (networkTrafficInFlight) return;
  networkTrafficInFlight = true;
  try {
    const sample = await api("/api/network/traffic");
    if (!sample.available) {
      networkTrafficPrevious = null;
      setValue("#traffic-rx-rate", "--", "muted");
      setValue("#traffic-tx-rate", "--", "muted");
      setValue("#traffic-session-rx", "--", "muted");
      setValue("#traffic-session-tx", "--", "muted");
      setValue("#traffic-session-total", "--", "muted");
      return;
    }

    let rxRate = 0;
    let txRate = 0;
    const previous = networkTrafficPrevious;
    if (previous && previous.interface === sample.interface) {
      const elapsed = (Number(sample.sampled_at_ms) - Number(previous.sampled_at_ms)) / 1000;
      if (elapsed > 0) {
        rxRate = Math.max(0, Number(sample.rx_bytes) - Number(previous.rx_bytes)) / elapsed;
        txRate = Math.max(0, Number(sample.tx_bytes) - Number(previous.tx_bytes)) / elapsed;
      }
    }
    networkTrafficPrevious = sample;
    setValue("#traffic-rx-rate", `${formatTrafficBytes(rxRate)}/s`, "neutral");
    setValue("#traffic-tx-rate", `${formatTrafficBytes(txRate)}/s`, "neutral");
    setValue("#traffic-session-rx", formatTrafficBytes(sample.session_rx_bytes), "neutral");
    setValue("#traffic-session-tx", formatTrafficBytes(sample.session_tx_bytes), "neutral");
    setValue("#traffic-session-total", formatTrafficBytes(sample.session_total_bytes), "emphasis");
    $("#traffic-session-total").title = "本次启动期间的下载与上传流量之和";
  } catch (error) {
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-total", "--", "muted");
  } finally {
    networkTrafficInFlight = false;
  }
}

function setNetworkTrafficPolling(enabled) {
  clearInterval(networkTrafficTimer);
  networkTrafficTimer = null;
  if (!enabled) {
    networkTrafficPrevious = null;
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-rx", "--", "muted");
    setValue("#traffic-session-tx", "--", "muted");
    setValue("#traffic-session-total", "--", "muted");
    return;
  }
  void loadNetworkTraffic();
  networkTrafficTimer = setInterval(loadNetworkTraffic, 1000);
}

async function loadESIM() {
  const list = $("#esim-list");
  const status = $("#esim-status");
  const download = $("#esim-download-section");
  const runtime = $("#esim-runtime-section");
  const profilePanel = $("#esim-profile-panel");
  const phonebook = $("#esim-phonebook-section");
  $("#esim-chip").hidden = true;
  $("#esim-chip").replaceChildren();
  runtime.hidden = true;
  download.hidden = false;
  profilePanel.hidden = false;
  phonebook.hidden = false;
  list.className = "list empty";
  list.textContent = "正在读取 eUICC";
  status.textContent = "正在读取卡片信息…";
  try {
    const overview = await api("/api/esim");
    if (overview.card_type === "physical_sim") {
      status.textContent = overview.message;
      list.textContent = overview.message;
      download.hidden = true;
      profilePanel.hidden = true;
      phonebook.hidden = true;
      setESIMHealthPolling(false);
      return;
    }
    const notesResponse = await api("/api/esim/module-notes");
    const notes = notesResponse.notes || {};
    const profiles = profileRows(overview);
    const eidRows = renderESIMEIDList(overview);
    const eidPanel = renderESIMEIDPanel(eidRows);
    renderESIMChip(overview);
    const profileCount = profiles.length;
    const eidCount = esimEIDRows(overview).length;
    const active = activeProfile(profiles);
    status.textContent = active
      ? `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 当前使用 ${profileDisplayName(active)}`
      : `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 未发现已启用 Profile`;
    if (!profiles.length) {
      if (eidRows.length) {
        list.className = "list";
        list.replaceChildren(eidPanel);
        return;
      }
      list.textContent = "未发现 eUICC/eSIM 卡片参数";
      return;
    }
    list.className = "list";
    const profileItems = profiles.map((profile) => {
      const note = notes[profile.iccid] || {};
      const row = document.createElement("article");
      row.className = `item esim-profile ${profile.state === 1 ? "active" : ""}`;
      const name = document.createElement("strong");
      name.textContent = note.label || profileDisplayName(profile);
      const detail = document.createElement("p");
      detail.textContent = [
        note.label && note.label !== profileDisplayName(profile) ? `卡内名称：${profileDisplayName(profile)}` : "",
        profile.service_provider_name ? `服务商：${profile.service_provider_name}` : "",
        profile.class_text ? `类型：${profile.class_text}` : "",
        note.tags ? `标签：${note.tags}` : "",
      ].filter(Boolean).join("\n");
      const metadata = document.createElement("div");
      metadata.className = "profile-metadata";
      if (note.phone) {
        const phoneRow = document.createElement("div");
        phoneRow.className = "profile-identifier-row";
        const phone = document.createElement("code");
        phone.className = "profile-iccid";
        phone.textContent = `模块号码 ${maskPhoneNumber(note.phone)}`;
        const revealPhone = document.createElement("button");
        revealPhone.className = "secondary compact profile-toggle-button";
        revealPhone.type = "button";
        revealPhone.textContent = "显示";
        revealPhone.addEventListener("click", () => {
          const hidden = revealPhone.textContent === "显示";
          phone.textContent = `模块号码 ${hidden ? note.phone : maskPhoneNumber(note.phone)}`;
          revealPhone.textContent = hidden ? "隐藏" : "显示";
        });
        const copyPhone = document.createElement("button");
        copyPhone.className = "secondary compact profile-copy-button";
        copyPhone.type = "button";
        copyPhone.textContent = "复制号码";
        copyPhone.addEventListener("click", () => copyIdentifier(note.phone, "模块号码"));
        phoneRow.append(phone, revealPhone, copyPhone);
        metadata.append(phoneRow);
      }
      if (profile.iccid) {
        const iccidRow = document.createElement("div");
        iccidRow.className = "profile-identifier-row";
        const iccid = document.createElement("code");
        iccid.className = "profile-iccid";
        iccid.textContent = `ICCID ${maskIdentifier(profile.iccid)}`;
        const reveal = document.createElement("button");
        reveal.className = "secondary compact profile-toggle-button";
        reveal.type = "button";
        reveal.textContent = "显示";
        reveal.addEventListener("click", () => {
          const hidden = reveal.textContent === "显示";
          iccid.textContent = `ICCID ${hidden ? profile.iccid : maskIdentifier(profile.iccid)}`;
          reveal.textContent = hidden ? "隐藏" : "显示";
        });
        const copy = document.createElement("button");
        copy.className = "secondary compact profile-copy-button";
        copy.type = "button";
        copy.textContent = "复制 ICCID";
        copy.addEventListener("click", () => copyIdentifier(profile.iccid, "ICCID"));
        iccidRow.append(iccid, reveal, copy);
        metadata.append(iccidRow);
      }
      const actionBox = document.createElement("div");
      actionBox.className = "profile-actions";
      if (profile.state !== 1) {
        const button = document.createElement("button");
        button.className = "compact";
        button.textContent = "启用";
        button.addEventListener("click", async () => {
          const label = profileDisplayName(profile);
          const confirmed = await showModal({
            title: "启用 Profile",
            message: `确定启用 ${label} 吗？当前正在使用的 eSIM Profile 会被切换。`,
            confirmLabel: "启用",
          });
          if (!confirmed) {
            return;
          }
          button.disabled = true;
          button.textContent = "切换中";
          try {
            const result = await api("/api/esim/switch", {
              method: "POST",
              body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }),
            });
            if (result.module_reboot_requested) {
              status.textContent = `已切换到 ${label}；模块正在重启，等待新 Profile 接管（约 ${result.reconnect_wait_seconds || 10} 秒）`;
              notice(`已切换 ${label}，模块正在重新读取新卡`);
              setTimeout(async () => {
                await loadESIM();
                await loadStatus();
              }, (result.reconnect_wait_seconds || 10) * 1000);
            } else {
              status.textContent = `Profile 已切换到 ${label}，但模块重启未确认：${result.module_reboot_warning || "请手动重启后再读取号码"}`;
              notice("Profile 已切换，模块重启未确认");
              await loadESIM();
            }
          } catch (error) {
            status.textContent = `切换失败：${error.message}`;
            notice(error.message);
            button.disabled = false;
            button.textContent = "启用";
          }
        });
        actionBox.append(button);
      } else {
        const button = document.createElement("button");
        button.className = "secondary compact";
        button.type = "button";
        button.textContent = "启用";
        button.disabled = true;
        actionBox.append(button);
      }
      const rename = document.createElement("button");
      rename.className = "secondary compact";
      rename.type = "button";
      rename.textContent = "改名";
      rename.addEventListener("click", async () => {
        const values = await showModal({
          title: "修改 Profile 名称",
          message: "名称将写入 eUICC 卡片内部的 Profile nickname。",
          confirmLabel: "保存",
          fields: [{ name: "name", label: "Profile 名称", value: profileDisplayName(profile), required: true }],
        });
        if (!values?.name) return;
        rename.disabled = true;
        try {
          await api("/api/esim/profile", { method: "PATCH", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "", name: values.name }) });
          notice("Profile 名称已修改");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { rename.disabled = false; }
      });
      const localNote = document.createElement("button");
      localNote.className = "secondary compact";
      localNote.type = "button";
      localNote.textContent = "模块资料";
      localNote.addEventListener("click", () => editProfileNote(profile, note));
      const remove = document.createElement("button");
      remove.className = "secondary danger compact";
      remove.type = "button";
      remove.textContent = "删除";
      remove.disabled = profile.state === 1;
      remove.addEventListener("click", async () => {
        const last4 = String(profile.iccid || "").slice(-4);
        const values = await showModal({
          title: "删除 Profile",
          message: `删除不可恢复。请输入 ICCID 后四位 ${last4} 确认。`,
          confirmLabel: "删除",
          danger: true,
          fields: [{ name: "confirmation", label: "ICCID 后四位", required: true }],
        });
        if (!values) return;
        if (values.confirmation !== last4) {
          notice("ICCID 后四位不匹配，未执行删除");
          return;
        }
        remove.disabled = true;
        try {
          await api("/api/esim/profile", { method: "DELETE", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }) });
          notice("Profile 已删除");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { remove.disabled = false; }
      });
      actionBox.append(localNote, rename, remove);
      const description = document.createElement("div");
      description.className = "profile-description";
      description.append(detail, metadata);
      row.append(name, description, actionBox);
      return row;
    });
    list.replaceChildren(...(eidPanel ? [eidPanel] : []), ...profileItems);
    void loadESIMHealth();
    setESIMHealthPolling(true);
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
    list.textContent = error.message;
    setESIMHealthPolling(false);
  }
}

const viewMetadata = {
  sms: { title:"短信", eyebrow:"MESSAGES", description:"接收问候，也不错过重要的验证码。" },
  calls: { title:"通话", eyebrow:"PHONE", description:"拨一通电话，让联系更近一点。" },
  network: { title:"网络概览", eyebrow:"OVERVIEW", description:"查看当前连接、信号和流量。" },
  settings: { title:"设备设置", eyebrow:"PREFERENCES", description:"按照你的习惯，照顾好每一个细节。" },
};
function showView(view, updateURL = true) {
  if (!viewMetadata[view]) view = "network";
  if(currentView !== view) window.scrollTo(0,0);
  currentView = view;
  window.webkit?.messageHandlers?.app?.postMessage({action:"viewChanged",view});
  document.querySelectorAll(".tab").forEach(tab => { const active = tab.dataset.view === view; tab.classList.toggle("active", active); if(active)tab.setAttribute("aria-current","page");else tab.removeAttribute("aria-current"); });
  document.querySelectorAll(".view").forEach(el => el.classList.toggle("active",el.id === view));
  $("#page-title").textContent=viewMetadata[view].title; $("#page-eyebrow").textContent=viewMetadata[view].eyebrow; $("#page-description").textContent=viewMetadata[view].description;
  if(updateURL){const url=new URL(location.href);url.searchParams.set("view",view);history.replaceState(null,"",url);}
  if(view === "calls"){markCallsSeen();void loadCalls();}
  if(view === "network")void loadNetwork();
  if(view === "settings"&&$("#esim-settings").open)void loadESIM();else setESIMHealthPolling(false);
}
document.querySelectorAll(".tab").forEach(tab=>tab.addEventListener("click",()=>showView(tab.dataset.view)));
window.addEventListener("popstate",()=>showView(new URLSearchParams(location.search).get("view"),false));
$("#esim-settings").addEventListener("toggle",()=>{if($("#esim-settings").open)void loadESIM();else setESIMHealthPolling(false);});

function openCompose(number = "") { $("#compose-modal").hidden=false;document.body.classList.add("modal-open");$("#phone").value=number;$("#send-status").textContent="";setTimeout(()=>$(number?"#message":"#phone").focus(),0); }
function closeCompose(){ $("#compose-modal").hidden=true;document.body.classList.remove("modal-open"); }
$("#new-sms").addEventListener("click",()=>openCompose()); $("#empty-new-sms").addEventListener("click",()=>openCompose());
$("#compose-close").addEventListener("click",closeCompose);$("#compose-modal").addEventListener("click",event=>{if(event.target===event.currentTarget)closeCompose();});
$("#message").addEventListener("input",()=>$("#message-length").textContent=$("#message").value.length);
$("#sms-search").addEventListener("input",renderSMS);
$("#sms-filter-all").addEventListener("click",()=>{smsFilter="all";renderSMS();});$("#sms-filter-unread").addEventListener("click",()=>{smsFilter="unread";renderSMS();});
$("#mark-sms-read").addEventListener("click",async()=>{try{await markSMSRead(smsMessages);}catch(error){notice(error.message);}});
$("#call-filter-all").addEventListener("click",()=>{callFilter="all";renderCallHistory(callHistoryRows);});$("#call-filter-missed").addEventListener("click",()=>{callFilter="missed";renderCallHistory(callHistoryRows);});
const keypadKeys=[["1",""],["2","ABC"],["3","DEF"],["4","GHI"],["5","JKL"],["6","MNO"],["7","PQRS"],["8","TUV"],["9","WXYZ"],["*",""],["0","+"],["#",""]];
$("#dialpad").replaceChildren(...keypadKeys.map(([key,letters])=>{const button=element("button","dial-key",key);button.type="button";button.dataset.key=key;button.setAttribute("aria-label",key);button.append(element("small","",letters));button.addEventListener("click",()=>{const field=$("#call-number");if(field.value.length<24)field.value+=key;});if(key==="0")button.addEventListener("contextmenu",event=>{event.preventDefault();if(!$("#call-number").value)$("#call-number").value="+";});return button;}));
$("#dial-backspace").addEventListener("click",()=>$("#call-number").value=$("#call-number").value.slice(0,-1));
$("#dial-call").addEventListener("click",()=>performCallAction("dial"));
$("#answer-call").addEventListener("click",()=>performCallAction("answer"));$("#banner-answer").addEventListener("click",()=>performCallAction("answer"));
$("#hangup-call").addEventListener("click",()=>performCallAction("hangup"));$("#banner-hangup").addEventListener("click",()=>performCallAction("hangup"));
$("#banner-view-call").addEventListener("click",()=>showView("calls"));
document.addEventListener("keydown",event=>{if(event.key==="Escape"&&!$("#compose-modal").hidden)closeCompose();});
$("#notification-settings").addEventListener("click",()=>{const bridge=window.webkit?.messageHandlers?.app;if(bridge)bridge.postMessage({action:"notificationSettings"});else notice(platform === 'windows' ? '在右下角托盘菜单中开启“短信与来电通知”，并在 Windows 通知设置中允许“碗里的菜”。' : '请在系统通知设置中，允许“碗里的菜”发送通知。');});
function notificationDescription(state) {
  if (state.authorization === "denied") return "系统通知未开启。请在系统设置中允许“碗里的菜”发送通知。";
  if (state.authorization !== "authorized") return "允许消息通知后，关闭窗口也能收到新短信和来电提醒。";
  if (!state.enabled) return "系统已允许通知，应用的消息提醒当前关闭。可在菜单栏中开启“消息通知”。";
  return "通知已开启，新短信与来电会在后台提醒你。";
}
window.addEventListener("wanli:notification-status",event=>{ $("#notification-description").textContent=notificationDescription(event.detail||{}); });
window.webkit?.messageHandlers?.app?.postMessage({action:"notificationStatus"});
showView(new URLSearchParams(location.search).get("view")||"network",false);

$("#esim-download-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const confirmed = await showModal({
    title: "下载新的 Profile",
    message: "将向 SM-DP+ 服务器下载并写入新的 eSIM Profile。写入期间请勿拔出模块。",
    confirmLabel: "开始下载",
  });
  if (!confirmed) return;
  const button = event.currentTarget.querySelector("button[type=submit]");
  const status = $("#esim-download-status");
  button.disabled = true;
  status.textContent = "正在下载并写入 Profile，请勿拔出模块...";
  try {
    const result = await api("/api/esim/download", { method: "POST", body: JSON.stringify({
      smdp: $("#esim-smdp").value, matching_id: $("#esim-matching-id").value,
      confirmation_code: $("#esim-confirmation-code").value, imei: $("#esim-imei").value, aid: $("#esim-aid").value,
    }) });
    status.textContent = result.message || "Profile 下载完成，正在重新读取卡片";
    notice("Profile 下载完成");
    await loadESIM();
  } catch (error) { status.textContent = `下载失败：${error.message}`; notice(error.message); } finally { button.disabled = false; }
});

$("#send-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter || event.currentTarget.querySelector("button[type=submit]");
  const originalLabel = button.textContent;
  button.disabled = true;
  button.textContent = "发送中";
  try {
    const result = await api("/api/sms/send", {
      method: "POST",
      body: JSON.stringify({ phone: $("#phone").value, message: $("#message").value }),
    });
    $("#message").value = "";
    $("#message-length").textContent = "0";
    closeCompose();
    const segments = Number(result.segments || 1);
    notice(segments > 1 ? `短信已发送（${segments} 个分片）` : "短信已发送");
    await loadSMS();
  } catch (error) {
    $("#send-status").textContent = `发送失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = originalLabel;
    button.prepend(icon("send"));
  }
});

$("#at-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const output = $("#at-output");
  output.textContent = "执行中";
  try {
    const result = await api("/api/at", {
      method: "POST",
      body: JSON.stringify({ command: $("#at-command").value }),
    });
    output.textContent = result.response || "OK";
  } catch (error) {
    output.textContent = error.message;
  }
});

$("#refresh").addEventListener("click", async () => {
  const tasks = [loadStatus(), loadSMS(), loadCalls()];
  if (currentView === "network") tasks.push(loadNetwork());
  await Promise.all(tasks);
  notice("状态已刷新");
});
$("#refresh-sms").addEventListener("click", async () => {
  const button = $("#refresh-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在读取短信...";
  try {
    const result = await api("/api/sms/refresh", { method: "POST" });
    await loadSMS();
    $("#sms-status").textContent = `短信读取完成：${result.count ?? "未知"} 条`;
    notice("短信读取完成");
  } catch (error) {
    $("#sms-status").textContent = `读取短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#clear-module-sms").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "清空模块旧短信",
    message: "只会清空模块内部 ME 存储里的旧短信，不会删除 SIM 卡短信。",
    confirmLabel: "确认清空",
    danger: true,
  });
  if (!confirmed) return;
  const button = $("#clear-module-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在清空模块内部旧短信...";
  try {
    const result = await api("/api/sms/clear-module", { method: "POST" });
    $("#sms-status").textContent = `模块旧短信已清理：${result.before ?? 0} -> ${result.after ?? 0} 条`;
    await loadSMS();
    notice("模块旧短信已清理");
  } catch (error) {
    $("#sms-status").textContent = `清理模块旧短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#refresh-esim").addEventListener("click", loadESIM);
$("#probe-esim-phonebook").addEventListener("click", probeESIMPhonebook);
$("#refresh-network").addEventListener("click", loadNetwork);
$("#check-4g-route").addEventListener("click", () =>
  runNetworkCheck("4G 出口", "/api/network/check-4g", $("#check-4g-route")));
$("#check-proxy-route").addEventListener("click", () =>
  runNetworkCheck("代理", "/api/network/check-proxy", $("#check-proxy-route")));
async function initializePlatform() {
  try { const health=await api("/api/health");platform=health.platform||platform;$("#demo-notice").hidden=health.demo!==true; } catch (_) {}
  $("#platform-network-hint").textContent=platform==="windows"?"Windows 上网使用 RNDIS 模式。首次连接可能需要安装网络与 USB AT 驱动。切换模式会让网络短暂中断。":"Mac 上网使用 ECM 模式。切换模式会让模块重新连接，网络可能短暂中断。";
  if(platform === "windows") $("#notification-description").textContent = "提醒由后台托盘应用发送。可在托盘菜单中开启或关闭“短信与来电通知”。";
  await loadStatus();
}
async function switchUSBMode(mode) {
  const confirmed=await showModal({title:mode===0?"切换为仅短信模式":"开启上网模式",message:"模块会重新连接，期间网络可能短暂中断。是否继续？",confirmLabel:"切换模式"});if(!confirmed)return;
  const buttons=[$("#enable-rndis"),$("#enable-sms-mode")];buttons.forEach(button=>button.disabled=true);const status=$("#usb-mode-status");
  try{status.textContent="正在设置模块模式…";await api("/api/network/usbnet",{method:"POST",body:JSON.stringify({mode})});await api("/api/network/reboot-module",{method:"POST"});status.textContent=mode===0?"已设置仅短信模式，等待模块重新连接。":"已设置上网模式，等待模块重新连接（约 15–30 秒）。";notice(status.textContent);setTimeout(()=>{void loadStatus();void loadNetwork();},20000);}
  catch(error){status.textContent=`设置失败：${error.message}`;notice(status.textContent);}finally{buttons.forEach(button=>button.disabled=false);}
}
$("#enable-rndis").addEventListener("click",()=>switchUSBMode(platform==="windows"?3:1));$("#enable-sms-mode").addEventListener("click",()=>switchUSBMode(0));
void initializePlatform();void loadSMS();void loadCalls();void loadEvents();void loadCellularPolicy();
setInterval(loadStatus,10000);setInterval(loadSMS,5000);setInterval(loadCalls,2000);setInterval(loadEvents,2500);setInterval(loadCellularPolicy,10000);
