"use strict";
const $ = (id) => document.getElementById(id);
const words = {
  zh: {
    loginTitle: "让业务资料，有迹可循。",
    loginIntro: "消息、附件与处理记录，保存在你自己的收件箱。",
    accessToken: "管理员访问令牌",
    tokenHint: "使用初始化时生成的私有配置中的 admin_token。",
    signIn: "打开收件箱",
    localFirst: "自主托管 · 无需 AI 账号",
    brandSub: "业务资料，可靠留存",
    yourData: "你的资料，你的存储",
    signOut: "退出",
    workspace: "工作空间",
    allInbox: "全部收件",
    attention: "待关注",
    connections: "连接与处理程序",
    bots: "收件来源",
    independent: "先留存，再处理。\n不依赖任何一种 Agent。",
    eyebrow: "YOUR BUSINESS INBOX",
    inboxTitle: "每一份资料，都有来处。",
    inboxIntro: "统一收件、保存原件，让后续处理有据可查。",
    refresh: "刷新收件",
    totalMessages: "已收消息",
    totalAttachments: "留存附件",
    pendingArchive: "正在归档",
    needsAttention: "需要关注",
    allTypes: "全部类型",
    text: "文字",
    file: "文件",
    image: "图片",
    mixed: "图文",
    voice: "语音",
    allStatuses: "全部状态",
    ready: "已归档",
    pending: "等待处理",
    partial: "部分失败",
    failed: "失败",
    filters: "筛选",
    export: "导出",
    senderId: "发送人 ID",
    fromDate: "起始日期",
    toDate: "结束日期",
    apply: "应用筛选",
    messageAndSource: "资料与来源",
    archiveStatus: "归档状态",
    receivedAt: "收到时间",
    emptyTitle: "收件箱已准备好",
    emptyText: "接入企业微信机器人或微信客服后，收到的消息和附件会显示在这里。",
    viewConnections: "查看连接配置",
    loadMore: "加载更多",
    scopeNote: "仅保存已接入来源实际收到的资料；微信客服接收一对一会话，不读取其他微信群。",
    connectionsIntro: "机器人与微信客服统一收件，外部程序各自处理。资料始终留在这里。",
    consumers: "处理程序",
    configure: "如何添加连接",
    configureText:
      "在私有 config.toml 中配置 bots、wechat_kf 和 consumers。微信客服另需配置 HTTPS 回调。有效的配置修改会自动加载。请妥善保管密钥。",
    noModel: "只使用归档功能时，无需配置模型或处理程序。",
    record: "ARCHIVE RECORD",
    search: "搜索消息内容或文件名",
    noMatches: "没有匹配的资料",
    changeFilters: "试试其他关键词，或调整筛选条件。",
    count: "条资料",
    attachments: "附件",
    source: "来源",
    sender: "发送人",
    conversation: "会话",
    content: "消息内容",
    quote: "引用内容",
    download: "下载",
    noText: "无文字内容",
    deliveries: "资料投递",
    results: "处理记录",
    noDeliveries: "未配置订阅，资料仍可通过 API 或导出读取。",
    noResults: "尚无处理结果。",
    retry: "重新投递",
    attempts: "次尝试",
    delivered: "已送达",
    completed: "处理完成",
    uncertain: "发送结果不确定",
    expired: "已过期",
    replyState: "回复状态",
    addResult: "登记处理结果",
    summary: "填写处理说明",
    reply: "同时回复原会话",
    saveResult: "登记结果",
    saved: "处理结果已登记",
    queued: "已加入投递队列",
    connected: "已连接",
    listening: "等待新消息",
    sync_failed: "收件同步失败",
    accepted: "平台已受理",
    wechat_kf: "微信客服",
    wecom_bot: "企业微信机器人",
    connecting: "连接中",
    disconnected: "未连接",
    disabled: "已停用",
    stopped: "已停止",
    auth_failed: "认证失败",
    noBots: "还没有配置收件来源。",
    noConsumers: "还没有配置处理程序。独立收件和查阅不受影响。",
    botScope: "收件来源范围",
    replyAllowed: "允许回传回复",
    replyDenied: "只登记结果",
    updated: "收件箱已更新",
    requestFailed: "请求失败",
    loginFailed: "登录失败，请检查访问令牌",
    loading: "正在读取…",
  },
  en: {
    loginTitle: "Every file. A clear trail.",
    loginIntro:
      "Messages, attachments and processing records, in an inbox you own.",
    accessToken: "Administrator access token",
    tokenHint:
      "Use admin_token from the private configuration created by init.",
    signIn: "Open your inbox",
    localFirst: "Self-hosted · No AI account required",
    brandSub: "Business records, kept reliably",
    yourData: "Your records. Your storage.",
    signOut: "Sign out",
    workspace: "WORKSPACE",
    allInbox: "All messages",
    attention: "Needs attention",
    connections: "Connections",
    bots: "SOURCES",
    independent: "Keep the source. Choose the processor.",
    eyebrow: "YOUR BUSINESS INBOX",
    inboxTitle: "Every record has a story.",
    inboxIntro: "Receive once, keep the original, and track what happens next.",
    refresh: "Refresh",
    totalMessages: "Messages received",
    totalAttachments: "Attachments",
    pendingArchive: "Archiving",
    needsAttention: "Needs attention",
    allTypes: "All types",
    text: "Text",
    file: "File",
    image: "Image",
    mixed: "Mixed",
    voice: "Voice",
    allStatuses: "All statuses",
    ready: "Archived",
    pending: "Pending",
    partial: "Partial failure",
    failed: "Failed",
    filters: "Filters",
    export: "Export",
    senderId: "Sender ID",
    fromDate: "From date",
    toDate: "To date",
    apply: "Apply filters",
    messageAndSource: "RECORD & SOURCE",
    archiveStatus: "STATUS",
    receivedAt: "RECEIVED",
    emptyTitle: "Your inbox is ready",
    emptyText:
      "Connect a WeCom bot or WeChat customer service. Received records appear here.",
    viewConnections: "View connections",
    loadMore: "Load more",
    scopeNote:
      "Archives records from connected sources. WeChat customer service receives direct conversations, not other WeChat groups.",
    connectionsIntro:
      "Bots and WeChat customer service receive. Independent consumers process. Your records stay here.",
    consumers: "Consumers",
    configure: "Adding a connection",
    configureText:
      "Configure bots, wechat_kf and consumers in your private config.toml. Customer service also needs an HTTPS callback. Valid changes reload automatically. Keep secrets private.",
    noModel: "No model or consumer is needed to receive and archive records.",
    record: "ARCHIVE RECORD",
    search: "Search messages or filenames",
    noMatches: "No matching records",
    changeFilters: "Try another search or adjust your filters.",
    count: "records",
    attachments: "Attachments",
    source: "Source",
    sender: "Sender",
    conversation: "Conversation",
    content: "Message",
    quote: "Quoted content",
    download: "Download",
    noText: "No text content",
    deliveries: "Delivery status",
    results: "Processing results",
    noDeliveries:
      "No webhook subscriptions. Records remain available through the API and export.",
    noResults: "No processing results yet.",
    retry: "Replay event",
    attempts: "attempts",
    delivered: "Delivered",
    completed: "Completed",
    uncertain: "Delivery uncertain",
    expired: "Expired",
    replyState: "Reply status",
    addResult: "Record a result",
    summary: "Describe the processing result",
    reply: "Also reply to the original chat",
    saveResult: "Save result",
    saved: "Processing result saved",
    queued: "Event queued for delivery",
    connected: "Connected",
    listening: "Listening",
    sync_failed: "Sync failed",
    accepted: "Accepted by platform",
    wechat_kf: "WeChat customer service",
    wecom_bot: "WeCom bot",
    connecting: "Connecting",
    disconnected: "Disconnected",
    disabled: "Disabled",
    stopped: "Stopped",
    auth_failed: "Authentication failed",
    noBots: "No bots configured yet.",
    noConsumers: "No consumers configured. Your standalone inbox still works.",
    botScope: "Bot scope",
    replyAllowed: "Replies permitted",
    replyDenied: "Record results only",
    updated: "Inbox refreshed",
    requestFailed: "Request failed",
    loginFailed: "Sign-in failed. Check your access token.",
    loading: "Loading…",
  },
};
let lang =
  localStorage.getItem("inbox-language") ||
  (navigator.language.startsWith("zh") ? "zh" : "en");
let csrf = "",
  messages = [],
  nextCursor = "",
  selected = null,
  bots = [],
  activeBot = "",
  view = "inbox",
  attention = false,
  resultKey = crypto.randomUUID(),
  loadSequence = 0;
const t = (key) => words[lang][key] || key;
const el = (tag, className, text) => {
  const n = document.createElement(tag);
  if (className) n.className = className;
  if (text !== undefined) n.textContent = text;
  return n;
};
const statusBadge = (s) => el("span", "status " + s, t(s));
const timeText = (v) =>
  new Date(v).toLocaleString(lang === "zh" ? "zh-CN" : "en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
const sizeText = (n) =>
  n >= 1048576
    ? (n / 1048576).toFixed(1) + " MB"
    : n >= 1024
      ? (n / 1024).toFixed(1) + " KB"
      : n + " B";
function toast(text) {
  $("toast").textContent = text;
  $("toast").hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => ($("toast").hidden = true), 4000);
}
function localize() {
  document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  document
    .querySelectorAll("[data-i18n]")
    .forEach((n) => (n.textContent = t(n.dataset.i18n)));
  document
    .querySelectorAll(".language")
    .forEach((n) => (n.textContent = lang === "zh" ? "English" : "中文"));
  $("search").placeholder = t("search");
  $("search").setAttribute("aria-label", t("search"));
  renderMessages();
  renderBotNav();
  if (selected) openDetail(selected, false);
}
async function api(path, options = {}) {
  const headers = { ...options.headers };
  if (options.body) {
    headers["Content-Type"] = "application/json";
    headers["X-CSRF-Token"] = csrf;
  }
  const response = await fetch(path, { ...options, headers });
  let data;
  try {
    data = await response.json();
  } catch {
    throw Error(t("requestFailed"));
  }
  if (!response.ok) {
    if (response.status === 401 && path != "/login") {
      showLogin();
    }
    throw Error(data.error?.message || t("requestFailed"));
  }
  return data;
}
function showLogin() {
  $("workspace").hidden = true;
  $("login").hidden = false;
  csrf = "";
}
function showWorkspace() {
  $("workspace").hidden = false;
  $("login").hidden = true;
  $("token").value = "";
}
function params() {
  const p = new URLSearchParams();
  if (activeBot) p.set("bot_id", activeBot);
  if ($("search").value) p.set("q", $("search").value);
  if ($("type-filter").value) p.set("type", $("type-filter").value);
  if (attention) p.set("status", "attention");
  else if ($("status-filter").value) p.set("status", $("status-filter").value);
  if ($("sender-filter").value) p.set("sender_id", $("sender-filter").value);
  for (const [id, key, end] of [
    ["from-filter", "from", false],
    ["to-filter", "to", true],
  ]) {
    const v = $(id).value;
    if (v) {
      const date = new Date(v + (end ? "T23:59:59.999" : "T00:00:00"));
      p.set(key, date.toISOString());
    }
  }
  return p;
}
async function refresh(append = false) {
  const sequence = ++loadSequence;
  try {
    const p = params();
    if (append && nextCursor) p.set("cursor", nextCursor);
    const [page, stats, botList] = await Promise.all([
      api("/v1/messages?" + p),
      api("/v1/stats"),
      api("/v1/bots"),
    ]);
    if (sequence !== loadSequence) return;
    messages = append ? messages.concat(page.messages) : page.messages;
    nextCursor = page.next_cursor || "";
    bots = botList;
    for (const key of ["messages", "attachments", "pending", "attention"])
      $("stat-" + key).textContent = stats[key].toLocaleString();
    $("nav-count").textContent = stats.messages;
    renderMessages();
    renderBotNav();
    if (view === "connections") await renderConnections();
    return true;
  } catch (e) {
    toast(e.message);
    return false;
  }
}
function renderMessages() {
  const list = $("message-list");
  list.replaceChildren();
  const shown = attention
    ? messages.filter((m) => m.status === "failed" || m.status === "partial")
    : messages;
  for (const m of shown) {
    const row = el(
      "button",
      "message-row" + (m.id === selected ? " selected" : ""),
    );
    row.setAttribute(
      "aria-label",
      (m.attachments[0]?.name || m.content || t(m.type)).slice(0, 120),
    );
    const main = el("div", "row-main");
    main.append(el("div", "file-icon", m.attachments.length ? "FILE" : "TXT"));
    const copy = el("div", "row-copy");
    copy.append(
      el("div", "row-title", m.attachments[0]?.name || m.content || t(m.type)),
    );
    const source = bots.find((b) => b.id === m.bot_id)?.name || m.bot_id;
    copy.append(
      el(
        "div",
        "row-sub",
        source +
          " · " +
          m.sender_id +
          (m.attachments.length
            ? " · " + m.attachments.length + " " + t("attachments")
            : ""),
      ),
    );
    main.append(copy);
    row.append(
      main,
      statusBadge(m.status),
      el("div", "row-time", timeText(m.received_at)),
    );
    row.onclick = () => openDetail(m.id);
    list.append(row);
  }
  $("empty").hidden = shown.length > 0;
  const filtered = params().toString() || attention;
  $("empty").querySelector("h2").textContent = t(
    filtered ? "noMatches" : "emptyTitle",
  );
  $("empty").querySelector("p").textContent = t(
    filtered ? "changeFilters" : "emptyText",
  );
  $("empty-setup").hidden = !!filtered;
  $("list-summary").textContent = shown.length + " " + t("count");
  $("load-more").hidden = !nextCursor;
}
function renderBotNav() {
  const nav = $("bot-nav");
  nav.replaceChildren();
  for (const b of bots) {
    const button = el(
      "button",
      "nav-item" + (activeBot === b.id ? " active" : ""),
    );
    button.append(
      el("span", "bot-dot" + (["connected", "listening"].includes(b.state) ? " online" : "")),
      el("span", "bot-nav-name", b.name || b.id),
    );
    button.onclick = () => {
      activeBot = b.id;
      attention = false;
      switchView("inbox");
      refresh();
    };
    nav.append(button);
  }
}
function switchView(which) {
  view = which;
  $("inbox-view").hidden = which !== "inbox";
  $("connections-view").hidden = which !== "connections";
  $("nav-inbox").classList.toggle(
    "active",
    which === "inbox" && !attention && !activeBot,
  );
  $("nav-attention").classList.toggle("active", which === "inbox" && attention);
  $("nav-connections").classList.toggle("active", which === "connections");
  if (which === "connections")
    renderConnections().catch((e) => toast(e.message));
}
async function renderConnections() {
  const bc = $("bot-cards");
  bc.replaceChildren();
  for (const b of bots) {
    const card = el("div", "connection-card");
    card.append(
      el("h3", "", b.name || b.id),
      el("p", "muted", t(b.kind || "wecom_bot")),
      statusBadge(b.state),
      el("p", "", b.id),
    );
    bc.append(card);
  }
  if (!bots.length) bc.append(el("p", "muted", t("noBots")));
  const consumers = await api("/v1/consumers");
  const cc = $("consumer-cards");
  cc.replaceChildren();
  for (const c of consumers) {
    const card = el("div", "connection-card");
    card.append(
      el("h3", "", c.id),
      el("p", "", t("botScope") + ": " + c.bots.join(", ")),
      el("p", "", t(c.allow_reply ? "replyAllowed" : "replyDenied")),
    );
    cc.append(card);
  }
  if (!consumers.length) cc.append(el("p", "muted", t("noConsumers")));
}
function section(title) {
  const div = el("section", "detail-section");
  div.append(el("h3", "", title));
  return div;
}
async function openDetail(id, focus = true) {
  selected = id;
  try {
    const data = await api("/v1/messages/" + id);
    if (selected !== id) return;
    const m = data.message;
    const content = $("detail-content");
    content.replaceChildren();
    content.append(
      el("h2", "", m.attachments[0]?.name || t(m.type)),
      statusBadge(m.status),
    );
    const meta = el("dl", "detail-meta");
    for (const [key, value] of [
      [t("source"), bots.find((b) => b.id === m.bot_id)?.name || m.bot_id],
      [t("sender"), m.sender_id],
      [t("conversation"), m.chat_id],
      [t("receivedAt"), new Date(m.received_at).toLocaleString()],
      ["ID", String(m.id)],
    ]) {
      meta.append(el("dt", "", key), el("dd", "", value));
    }
    content.append(meta);
    if (m.error) content.append(el("p", "error", m.error));
    if (m.content || m.quote) {
      const block = section(t("content"));
      if (m.content) block.append(el("div", "message-body", m.content));
      if (m.quote) block.append(el("div", "quote", m.quote));
      content.append(block);
    }
    if (m.attachments.length) {
      const block = section(t("attachments"));
      for (const a of m.attachments) {
        const row = el("div", "attachment");
        row.append(el("div", "file-icon", "FILE"));
        const info = el("div", "attachment-info");
        info.append(
          el("div", "attachment-name", a.name),
          el(
            "small",
            "",
            a.status === "ready" ? sizeText(a.size) : a.error || t(a.status),
          ),
        );
        row.append(info);
        if (a.status === "ready") {
          const link = el("a", "", t("download"));
          link.href = "/v1/attachments/" + encodeURIComponent(a.id);
          row.append(link);
        } else row.append(statusBadge(a.status));
        block.append(row);
      }
      content.append(block);
    }
    const deliveries = section(t("deliveries"));
    if (!data.deliveries.length)
      deliveries.append(el("p", "muted", t("noDeliveries")));
    for (const d of data.deliveries) {
      const div = el("div", "delivery"),
        head = el("div", "delivery-head");
      head.append(el("strong", "", d.consumer_id), statusBadge(d.status));
      div.append(
        head,
        el(
          "p",
          "",
          d.attempts + " " + t("attempts") + (d.error ? " · " + d.error : ""),
        ),
      );
      if (["failed", "delivered"].includes(d.status)) {
        const btn = el("button", "button", t("retry"));
        btn.onclick = async () => {
          btn.disabled = true;
          try {
            await api("/v1/deliveries/" + d.id + "/retry", {
              method: "POST",
              body: "{}",
            });
            toast(t("queued"));
            openDetail(id, false);
          } catch (e) {
            toast(e.message);
            btn.disabled = false;
          }
        };
        div.append(btn);
      }
      deliveries.append(div);
    }
    content.append(deliveries);
    const results = section(t("results"));
    if (!data.results.length) results.append(el("p", "muted", t("noResults")));
    for (const r of data.results) {
      const div = el("div", "result");
      div.append(
        el("strong", "", r.consumer_id + " · " + t(r.status)),
        el("p", "", r.summary),
      );
      if (r.reply_status)
        div.append(
          el(
            "p",
            "",
            t("replyState") +
              ": " +
              t(r.reply_status) +
              (r.reply_error ? " · " + r.reply_error : ""),
          ),
        );
      results.append(div);
    }
    content.append(results);
    if (m.status !== "pending") {
      const block = section(t("addResult"));
      const textarea = el("textarea");
      textarea.placeholder = t("summary");
      textarea.setAttribute("aria-label", t("summary"));
      textarea.maxLength = 2000;
      const controls = el("div", "result-controls");
      const select = el("select");
      for (const status of ["completed", "failed"]) {
        const option = el("option", "", t(status));
        option.value = status;
        select.append(option);
      }
      const label = el("label"),
        check = el("input");
      check.type = "checkbox";
      label.append(check, document.createTextNode(t("reply")));
      const save = el("button", "primary", t("saveResult"));
      save.onclick = async () => {
        save.disabled = true;
        try {
          await api("/v1/messages/" + id + "/results", {
            method: "POST",
            body: JSON.stringify({
              idempotency_key: resultKey,
              status: select.value,
              summary: textarea.value,
              reply: check.checked,
            }),
          });
          resultKey = crypto.randomUUID();
          toast(t("saved"));
          openDetail(id, false);
        } catch (e) {
          toast(e.message);
          save.disabled = false;
        }
      };
      controls.append(select, label, save);
      block.append(textarea, controls);
      content.append(block);
    }
    $("detail").hidden = false;
    $("detail-backdrop").hidden = false;
    renderMessages();
    if (focus) $("close-detail").focus();
  } catch (e) {
    toast(e.message);
  }
}
function closeDetail() {
  selected = null;
  $("detail").hidden = true;
  $("detail-backdrop").hidden = true;
  renderMessages();
}
$("login-form").onsubmit = async (event) => {
  event.preventDefault();
  const button = event.currentTarget.querySelector("button");
  button.disabled = true;
  $("login-error").textContent = "";
  try {
    const result = await api("/login", {
      method: "POST",
      body: JSON.stringify({ token: $("token").value }),
    });
    csrf = result.csrf;
    showWorkspace();
    await refresh();
  } catch {
    $("login-error").textContent = t("loginFailed");
  } finally {
    button.disabled = false;
  }
};
$("logout").onclick = async () => {
  try {
    await api("/logout", { method: "POST", body: "{}" });
    showLogin();
  } catch (e) {
    toast(e.message);
  }
};
document.querySelectorAll(".language").forEach(
  (button) =>
    (button.onclick = () => {
      lang = lang === "zh" ? "en" : "zh";
      localStorage.setItem("inbox-language", lang);
      localize();
      if (view === "connections")
        renderConnections().catch((e) => toast(e.message));
    }),
);
$("refresh").onclick = () =>
  refresh().then((ok) => {
    if (ok) toast(t("updated"));
  });
$("load-more").onclick = () => refresh(true);
let searchTimer;
$("search").oninput = () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => refresh(), 300);
};
for (const id of ["type-filter", "status-filter"])
  $(id).onchange = () => {
    attention = false;
    switchView("inbox");
    refresh();
  };
$("filter-toggle").onclick = () =>
  ($("extra-filters").hidden = !$("extra-filters").hidden);
$("apply-filters").onclick = () => refresh();
$("export").onchange = (event) => {
  const format = event.target.value;
  if (!format) return;
  const p = params();
  p.set("format", format);
  window.location.assign("/v1/export?" + p);
  event.target.value = "";
};
$("nav-inbox").onclick = () => {
  activeBot = "";
  attention = false;
  $("status-filter").value = "";
  switchView("inbox");
  refresh();
};
$("nav-attention").onclick = () => {
  activeBot = "";
  attention = true;
  $("status-filter").value = "";
  switchView("inbox");
  refresh();
};
$("nav-connections").onclick = () => switchView("connections");
$("empty-setup").onclick = () => switchView("connections");
$("close-detail").onclick = closeDetail;
$("detail-backdrop").onclick = closeDetail;
document.onkeydown = (e) => {
  if (e.key === "Escape") closeDetail();
};
localize();
api("/v1/session")
  .then((data) => {
    csrf = data.csrf;
    showWorkspace();
    return refresh();
  })
  .catch(() => showLogin());
setInterval(() => {
  if (
    !$("workspace").hidden &&
    !document.hidden &&
    !selected &&
    document.activeElement?.tagName !== "INPUT"
  )
    refresh();
}, 15000);
