let selectedId = null;
let selectedAccount = ""; // "" = all mail, across every account
let selectedFolder = "inbox";
let currentMessage = null;

const listEl = document.getElementById("message-list");
const emptyState = document.getElementById("empty-state");
const detailEl = document.getElementById("detail");
const accountSelect = document.getElementById("account-select");
const folderTabs = document.getElementById("folder-tabs");
const accountDatalist = document.getElementById("account-datalist");

async function fetchJSON(url, opts) {
  const res = await fetch(url, opts);
  if (!res.ok && res.status !== 204) throw new Error(`${url}: ${res.status}`);
  return res.status === 204 ? null : res.json();
}

function fmtDate(iso) {
  const d = new Date(iso);
  return isNaN(d) ? iso : d.toLocaleString();
}

async function refreshAccounts() {
  const accounts = await fetchJSON("/api/accounts");

  const current = accountSelect.value;
  accountSelect.innerHTML = '<option value="">All mail</option>';
  accountDatalist.innerHTML = "";
  for (const a of accounts) {
    const opt = document.createElement("option");
    opt.value = a;
    opt.textContent = a;
    accountSelect.appendChild(opt);

    const dlOpt = document.createElement("option");
    dlOpt.value = a;
    accountDatalist.appendChild(dlOpt);
  }
  if (accounts.includes(current)) accountSelect.value = current;
}

async function refreshList() {
  const messages = selectedAccount
    ? await fetchJSON(`/api/accounts/${encodeURIComponent(selectedAccount)}/messages?folder=${selectedFolder}`)
    : await fetchJSON("/api/messages");

  listEl.innerHTML = "";
  for (const m of messages) {
    const li = document.createElement("li");
    li.className = m.seen ? "" : "unseen";
    if (m.id === selectedId) li.classList.add("selected");
    li.dataset.id = m.id;

    const subject = document.createElement("span");
    subject.className = "subject";
    subject.textContent = m.subject || "(no subject)";

    const meta = document.createElement("span");
    meta.className = "meta";
    const who = selectedFolder === "sent" && selectedAccount ? (m.to || []).join(", ") : m.from;
    meta.textContent = `${who} · ${fmtDate(m.date)}${m.hasAttachments ? " · 📎" : ""}`;

    li.append(subject, meta);
    li.addEventListener("click", () => selectMessage(m.id));
    listEl.appendChild(li);
  }
}

async function selectMessage(id) {
  selectedId = id;
  document.querySelectorAll("#message-list li").forEach(li => {
    li.classList.toggle("selected", li.dataset.id === id);
    if (li.dataset.id === id) li.classList.remove("unseen");
  });

  const m = await fetchJSON(`/api/messages/${id}`);
  currentMessage = m;

  emptyState.hidden = true;
  detailEl.hidden = false;

  document.getElementById("detail-subject").textContent = m.subject || "(no subject)";
  document.getElementById("detail-from").textContent = m.from;
  document.getElementById("detail-to").textContent = (m.to || []).join(", ");
  document.getElementById("detail-date").textContent = fmtDate(m.date);

  document.getElementById("html-frame").srcdoc = m.html || "<em>No HTML part</em>";
  document.getElementById("tab-text").textContent = m.text || "(no plain text part)";

  const raw = await fetch(`/api/messages/${id}/raw`).then(r => r.text());
  document.getElementById("tab-raw").textContent = raw;

  const attachmentsEl = document.getElementById("attachments");
  attachmentsEl.innerHTML = "";
  for (const a of m.attachments || []) {
    const li = document.createElement("li");
    const link = document.createElement("a");
    link.href = `/api/messages/${id}/attachments/${encodeURIComponent(a.filename)}`;
    link.textContent = `${a.filename} (${a.size} bytes)`;
    link.download = a.filename;
    li.appendChild(link);
    attachmentsEl.appendChild(li);
  }

  document.getElementById("detail-clear").onclick = async () => {
    await fetchJSON(`/api/messages/${id}`, { method: "DELETE" });
    selectedId = null;
    currentMessage = null;
    detailEl.hidden = true;
    emptyState.hidden = false;
    refreshList();
  };
}

document.querySelectorAll(".tab-btn").forEach(btn => {
  btn.addEventListener("click", () => {
    document.querySelectorAll(".tab-btn").forEach(b => b.classList.remove("active"));
    document.querySelectorAll(".tab-panel").forEach(p => p.classList.remove("active"));
    btn.classList.add("active");
    document.getElementById(`tab-${btn.dataset.tab}`).classList.add("active");
  });
});

document.getElementById("clear-all").addEventListener("click", async () => {
  await fetchJSON("/api/messages", { method: "DELETE" });
  selectedId = null;
  currentMessage = null;
  detailEl.hidden = true;
  emptyState.hidden = false;
  refreshList();
});

accountSelect.addEventListener("change", () => {
  selectedAccount = accountSelect.value;
  folderTabs.hidden = !selectedAccount;
  refreshList();
});

document.querySelectorAll(".folder-btn").forEach(btn => {
  btn.addEventListener("click", () => {
    document.querySelectorAll(".folder-btn").forEach(b => b.classList.remove("active"));
    btn.classList.add("active");
    selectedFolder = btn.dataset.folder;
    refreshList();
  });
});

// --- Compose / reply ---

const composeOverlay = document.getElementById("compose-overlay");
const composeForm = document.getElementById("compose-form");
const composeFrom = document.getElementById("compose-from");
const composeTo = document.getElementById("compose-to");
const composeSubject = document.getElementById("compose-subject");
const composeText = document.getElementById("compose-text");
const composeError = document.getElementById("compose-error");
let replyToId = null;

function openCompose({ from = "", to = "", subject = "", text = "", inReplyTo = null } = {}) {
  composeFrom.value = from;
  composeTo.value = to;
  composeSubject.value = subject;
  composeText.value = text;
  replyToId = inReplyTo;
  composeError.hidden = true;
  composeOverlay.hidden = false;
  (from ? composeTo : composeFrom).focus();
}

function closeCompose() {
  composeOverlay.hidden = true;
}

document.getElementById("compose-btn").addEventListener("click", () => {
  openCompose({ from: selectedAccount });
});

document.getElementById("compose-cancel").addEventListener("click", closeCompose);

// Close on backdrop click (but not when the click originated inside the form)
// or on Escape.
composeOverlay.addEventListener("click", (e) => {
  if (e.target === composeOverlay) closeCompose();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !composeOverlay.hidden) closeCompose();
});

document.getElementById("detail-reply").addEventListener("click", () => {
  if (!currentMessage) return;
  const m = currentMessage;
  const replyFrom = selectedAccount || (m.to && m.to[0]) || "";
  const subject = /^re:/i.test(m.subject || "") ? m.subject : `Re: ${m.subject || ""}`;
  const quoted = (m.text || "").split("\n").map(line => `> ${line}`).join("\n");
  openCompose({
    from: replyFrom,
    to: m.from,
    subject,
    text: `\n\nOn ${fmtDate(m.date)}, ${m.from} wrote:\n${quoted}`,
    inReplyTo: m.id,
  });
});

composeForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  composeError.hidden = true;
  try {
    const res = await fetch("/api/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        from: composeFrom.value.trim(),
        to: composeTo.value.trim(),
        subject: composeSubject.value.trim(),
        text: composeText.value,
        inReplyTo: replyToId || "",
      }),
    });
    if (!res.ok) throw new Error(await res.text());
    closeCompose();
    refreshAccounts();
    refreshList();
  } catch (err) {
    composeError.textContent = `Failed to send: ${err.message}`;
    composeError.hidden = false;
  }
});

refreshAccounts();
refreshList();

// EventSource reconnects automatically on drop; fall back to polling only
// if the browser doesn't support SSE at all.
if (typeof EventSource !== "undefined") {
  const events = new EventSource("/api/events");
  events.addEventListener("update", () => {
    refreshAccounts();
    refreshList();
  });
} else {
  setInterval(() => {
    refreshAccounts();
    refreshList();
  }, 3000);
}
