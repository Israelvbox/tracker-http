// app.js — helpers compartidos por todas las páginas

const API = ""; // mismo origen (Nginx hace proxy de /api y /announce al backend Go)

function fmtSize(bytes) {
  if (!bytes || bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let n = bytes;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return n.toFixed(n >= 10 || i === 0 ? 0 : 1) + " " + units[i];
}

function fmtDate(iso) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "—";
  const now = new Date();
  const diffMs = now - d;
  const diffDays = Math.floor(diffMs / 86400000);
  if (diffDays === 0) return "hoy";
  if (diffDays === 1) return "ayer";
  if (diffDays < 7) return diffDays + " días";
  return d.toISOString().slice(0, 10);
}

function escapeHtml(str) {
  const div = document.createElement("div");
  div.textContent = str ?? "";
  return div.innerHTML;
}

async function apiGet(path) {
  const res = await fetch(API + path, { credentials: "same-origin" });
  return res;
}

async function apiPost(path, body) {
  const res = await fetch(API + path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
  });
  return res;
}

// Comprueba sesión actual y actualiza la barra de navegación (#nav-slot en cada página)
async function refreshNav() {
  const slot = document.getElementById("nav-slot");
  if (!slot) return;
  try {
    const res = await apiGet("/api/me");
    const data = await res.json();
    if (data.logged_in) {
      slot.innerHTML = `
        <a href="/upload.html">subir torrent</a>
        <a href="/account.html">mi cuenta</a>
        <span class="user">${escapeHtml(data.username)}</span>
        <a href="#" id="logout-link">salir</a>
      `;
      document.getElementById("logout-link").addEventListener("click", async (e) => {
        e.preventDefault();
        await apiPost("/api/logout", {});
        window.location.href = "/";
      });
    } else {
      slot.innerHTML = `
        <a href="/login.html">acceder</a>
        <a href="/register.html">registrarse</a>
      `;
    }
  } catch (e) {
    slot.innerHTML = `<a href="/login.html">acceder</a> <a href="/register.html">registrarse</a>`;
  }
}

// Footer: torrents indexados + usuarios activos
async function refreshStats() {
  const elTorrents = document.getElementById("stat-torrents");
  const elUsers = document.getElementById("stat-users");
  if (!elTorrents && !elUsers) return;
  try {
    const res = await apiGet("/api/stats");
    const data = await res.json();
    if (elTorrents) elTorrents.textContent = data.total_torrents ?? "0";
    if (elUsers) elUsers.textContent = data.active_users ?? "0";
  } catch (e) {
    if (elTorrents) elTorrents.textContent = "—";
    if (elUsers) elUsers.textContent = "—";
  }
}

document.addEventListener("DOMContentLoaded", () => {
  refreshNav();
  refreshStats();
});
