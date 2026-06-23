package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Report representa una denuncia/flag sobre un torrent, hecha por cualquiera
// (registrado o anónimo).
type Report struct {
	ID          string    `json:"id"`
	InfoHash    string    `json:"info_hash"`
	TorrentName string    `json:"torrent_name"`
	Reason      string    `json:"reason"` // illegal | malicious | corrupt | fake | other
	Details     string    `json:"details"`
	ReporterIP  string    `json:"reporter_ip"`
	Username    string    `json:"username"` // vacío si es anónimo
	CreatedAt   time.Time `json:"created_at"`
	Resolved    bool      `json:"resolved"`
}

var validReasons = map[string]bool{
	"illegal":   true,
	"malicious": true,
	"corrupt":   true,
	"fake":      true,
	"other":     true,
}

// Límite simple anti-espam: máximo 5 reportes por IP cada 10 minutos.
// Esto se queda en memoria a propósito (es solo un contador efímero, no datos).
var (
	reportRateLimit      = make(map[string][]time.Time)
	reportRateLimitMutex sync.Mutex
)

func allowReport(ip string) bool {
	reportRateLimitMutex.Lock()
	defer reportRateLimitMutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-10 * time.Minute)

	times := reportRateLimit[ip]
	fresh := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}

	if len(fresh) >= 5 {
		reportRateLimit[ip] = fresh
		return false
	}

	fresh = append(fresh, now)
	reportRateLimit[ip] = fresh
	return true
}

func startReportRateLimitJanitor() {
	for range time.Tick(30 * time.Minute) {
		reportRateLimitMutex.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for ip, times := range reportRateLimit {
			fresh := times[:0]
			for _, t := range times {
				if t.After(cutoff) {
					fresh = append(fresh, t)
				}
			}
			if len(fresh) == 0 {
				delete(reportRateLimit, ip)
			} else {
				reportRateLimit[ip] = fresh
			}
		}
		reportRateLimitMutex.Unlock()
	}
}

// =========================================================
// ENDPOINT PÚBLICO: crear un reporte
// =========================================================

func reportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	ip := getClientIP(r)
	if !allowReport(ip) {
		jsonError(w, "Demasiados reportes desde tu IP. Espera unos minutos.", http.StatusTooManyRequests)
		return
	}

	var req struct {
		InfoHash string `json:"info_hash"`
		Reason   string `json:"reason"`
		Details  string `json:"details"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	req.InfoHash = strings.TrimSpace(req.InfoHash)
	if req.InfoHash == "" {
		jsonError(w, "Falta info_hash", http.StatusBadRequest)
		return
	}
	if !validReasons[req.Reason] {
		jsonError(w, "Motivo inválido", http.StatusBadRequest)
		return
	}
	if len(req.Details) > 1000 {
		req.Details = req.Details[:1000]
	}

	torrent, err := dbGetTorrentByHash(req.InfoHash)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if torrent == nil {
		jsonError(w, "Ese torrent no existe", http.StatusNotFound)
		return
	}

	username := getAuthenticatedUser(r) // puede ser "" si es anónimo

	rep := Report{
		ID:          generateToken()[:12],
		InfoHash:    req.InfoHash,
		TorrentName: torrent.Name,
		Reason:      req.Reason,
		Details:     strings.TrimSpace(req.Details),
		ReporterIP:  ip,
		Username:    username,
		CreatedAt:   time.Now(),
		Resolved:    false,
	}

	if err := dbInsertReport(rep); err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// =========================================================
// ADMIN: ver y resolver reportes
// =========================================================

func adminReportsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	showResolved := r.URL.Query().Get("resolved") == "true"

	out, err := dbListReports(showResolved)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if out == nil {
		out = []Report{}
	}
	json.NewEncoder(w).Encode(out)
}

func adminResolveReportHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID       string `json:"id"`
		Resolved bool   `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	found, err := dbResolveReport(req.ID, req.Resolved)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "Reporte no encontrado", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
