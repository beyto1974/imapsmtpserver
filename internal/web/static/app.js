let selectedId = null;

const listEl = document.getElementById("message-list");
const emptyState = document.getElementById("empty-state");
const detailEl = document.getElementById("detail");

async function fetchJSON(url, opts) {
  const res = await fetch(url, opts);
  if (!res.ok && res.status !== 204) throw new Error(`${url}: ${res.status}`);
  return res.status === 204 ? null : res.json();
}

function fmtDate(iso) {
  const d = new Date(iso);
  return isNaN(d) ? iso : d.toLocaleString();
}

async function refreshList() {
  const messages = await fetchJSON("/api/messages");
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
    meta.textContent = `${m.from} · ${fmtDate(m.date)}${m.hasAttachments ? " · 📎" : ""}`;

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
  detailEl.hidden = true;
  emptyState.hidden = false;
  refreshList();
});

refreshList();

// EventSource reconnects automatically on drop; fall back to polling only
// if the browser doesn't support SSE at all.
if (typeof EventSource !== "undefined") {
  const events = new EventSource("/api/events");
  events.addEventListener("update", refreshList);
} else {
  setInterval(refreshList, 3000);
}
