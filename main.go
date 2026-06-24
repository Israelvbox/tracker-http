package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Peer struct {
	IP       net.IP
	Port     uint16
	LastSeen time.Time
	Left     uint64
}

type TorrentMetadata struct {
	Name      string    `json:"name"`
	InfoHash  string    `json:"info_hash"`
	Size      int64     `json:"size"`
	Uploaded  time.Time `json:"uploaded"`
	Downloads int       `json:"downloads"`
	Uploader  string    `json:"uploader"`
}

// --- USUARIOS Y SESIONES ---
type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	RegisteredIP string    `json:"registered_ip"`
	LastLoginIP  string    `json:"last_login_ip"`
	LastLoginAt  time.Time `json:"last_login_at"`
}

type Session struct {
	Username  string
	ExpiresAt time.Time
}

var (
	sessions      = make(map[string]Session) // token -> session (en memoria; se pierde al reiniciar)
	sessionsMutex sync.Mutex

	peersMap sync.Map // info_hash (string 20 bytes) -> *sync.Map (peer_id -> Peer), siempre en memoria

	// El instalador (install.sh) sustituye __DOMAIN__ por el dominio real configurado.
	TrackerAnnounceURL = "https://__DOMAIN__/announce"

	// Token de administración. SE LEE DE LA VARIABLE DE ENTORNO ADMIN_TOKEN.
	adminToken string
)

const (
	AnnounceInterval = 600              // 10 minutos: cada cuánto deben re-anunciarse los clientes
	PeerExpiryTime   = 20 * time.Minute // debe ser MAYOR que AnnounceInterval, con margen
	SessionDuration  = 7 * 24 * time.Hour
	TorrentsDir      = "/var/www/tracker/public/torrents"
)

func main() {
	connectDB()
	defer db.Close()

	initAdminToken()
	os.MkdirAll(TorrentsDir, 0755)

	go startJanitor()
	go startSessionJanitor()

	http.HandleFunc("/announce", announceHandler)
	http.HandleFunc("/api/register", registerHandler)
	http.HandleFunc("/api/login", loginHandler)
	http.HandleFunc("/api/logout", logoutHandler)
	http.HandleFunc("/api/upload", uploadHandler)
	http.HandleFunc("/api/torrents", listTorrentsHandler)
	http.HandleFunc("/api/stats", globalStatsHandler)
	http.HandleFunc("/api/me", meHandler)

	http.HandleFunc("/api/admin/users", adminUsersHandler)
	http.HandleFunc("/api/admin/users/create", adminCreateUserHandler)
	http.HandleFunc("/api/admin/users/delete", adminDeleteUserHandler)
	http.HandleFunc("/api/admin/invites", adminListInvitesHandler)
	http.HandleFunc("/api/admin/invites/create", adminCreateInviteHandler)
	http.HandleFunc("/api/admin/invites/revoke", adminRevokeInviteHandler)
	http.HandleFunc("/api/admin/torrents", adminTorrentsHandler)
	http.HandleFunc("/api/admin/torrents/delete", adminDeleteTorrentHandler)
	http.HandleFunc("/api/admin/overview", adminOverviewHandler)

	fmt.Println("🚀 Tracker corriendo en puerto 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// initAdminToken lee ADMIN_TOKEN del entorno. Si no existe, genera uno aleatorio
// y lo imprime UNA vez en consola para que lo copies (no se guarda en disco).
func initAdminToken() {
	if t := os.Getenv("ADMIN_TOKEN"); t != "" {
		adminToken = t
		fmt.Println("🔑 Admin token cargado desde la variable de entorno ADMIN_TOKEN")
		return
	}
	adminToken = generateToken()
	fmt.Println("==========================================================")
	fmt.Println("⚠️  No se definió ADMIN_TOKEN. Token temporal generado:")
	fmt.Println("    " + adminToken)
	fmt.Println("    Cámbialo definiendo ADMIN_TOKEN como variable de entorno")
	fmt.Println("    permanente, o este token cambiará cada reinicio.")
	fmt.Println("==========================================================")
}

// requireAdmin valida el header X-Admin-Token. Devuelve true si es válido.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	token := r.Header.Get("X-Admin-Token")
	if token == "" || token != adminToken {
		jsonError(w, "Token de administrador inválido", http.StatusUnauthorized)
		return false
	}
	return true
}

// =========================================================
// AUTENTICACIÓN
// =========================================================

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generateInviteCode genera un código de invitación más corto y legible
// que un token completo (sigue siendo aleatorio y difícil de adivinar).
func generateInviteCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// getClientIP extrae la IP real del cliente, respetando X-Forwarded-For (proxy/Nginx).
func getClientIP(r *http.Request) string {
	ipStr := r.Header.Get("X-Forwarded-For")
	if ipStr != "" {
		return strings.TrimSpace(strings.Split(ipStr, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// registerHandler crea una cuenta SOLO si se aporta un código de invitación
// válido, sin usar y sin revocar. No hay registro abierto.
func registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.InviteCode = strings.TrimSpace(req.InviteCode)

	if req.InviteCode == "" {
		jsonError(w, "Necesitas un código de invitación para registrarte", http.StatusForbidden)
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 6 {
		jsonError(w, "Usuario mín. 3 caracteres, contraseña mín. 6", http.StatusBadRequest)
		return
	}

	exists, err := dbUserExists(req.Username)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, "El usuario ya existe", http.StatusConflict)
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		jsonError(w, "Error interno", http.StatusInternalServerError)
		return
	}

	if err := dbRegisterWithInvite(req.Username, hash, getClientIP(r), req.InviteCode); err != nil {
		if isUniqueViolation(err) {
			jsonError(w, "El usuario ya existe", http.StatusConflict)
			return
		}
		jsonError(w, err.Error(), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	found, err := dbGetUserByUsername(req.Username)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	if found == nil || !verifyPassword(req.Password, found.PasswordHash) {
		jsonError(w, "Usuario o contraseña incorrectos", http.StatusUnauthorized)
		return
	}

	if err := dbUpdateLastLogin(found.Username, getClientIP(r)); err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	token := generateToken()
	sessionsMutex.Lock()
	sessions[token] = Session{Username: found.Username, ExpiresAt: time.Now().Add(SessionDuration)}
	sessionsMutex.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Now().Add(SessionDuration),
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "username": found.Username})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		sessionsMutex.Lock()
		delete(sessions, cookie.Value)
		sessionsMutex.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session_token", Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusOK)
}

// meHandler permite al frontend saber si hay una sesión activa y de quién.
func meHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := getAuthenticatedUser(r)
	if username == "" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"logged_in": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"logged_in": true, "username": username})
}

// Devuelve el usuario autenticado o "" si no hay sesión válida
func getAuthenticatedUser(r *http.Request) string {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return ""
	}
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()
	s, ok := sessions[cookie.Value]
	if !ok || time.Now().After(s.ExpiresAt) {
		return ""
	}
	return s.Username
}

func startSessionJanitor() {
	for range time.Tick(10 * time.Minute) {
		sessionsMutex.Lock()
		for token, s := range sessions {
			if time.Now().After(s.ExpiresAt) {
				delete(sessions, token)
			}
		}
		sessionsMutex.Unlock()
	}
}

// =========================================================
// TRACKER (BitTorrent announce)
// =========================================================

func announceHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	infoHashRaw := query.Get("info_hash")
	peerID := query.Get("peer_id")
	portStr := query.Get("port")
	leftStr := query.Get("left")
	event := query.Get("event")

	if len(infoHashRaw) != 20 || len(peerID) != 20 || portStr == "" {
		http.Error(w, "d14:failure reason25:Invalid torrent parameters", http.StatusBadRequest)
		return
	}

	port, _ := strconv.ParseUint(portStr, 10, 16)
	left, _ := strconv.ParseUint(leftStr, 10, 64)

	actualPeers, _ := peersMap.LoadOrStore(infoHashRaw, &sync.Map{})
	pMap := actualPeers.(*sync.Map)

	if event == "stopped" {
		pMap.Delete(peerID)
		return
	}

	if event == "completed" {
		hashHex := fmt.Sprintf("%x", infoHashRaw)
		if err := dbIncrementDownloads(hashHex); err != nil {
			log.Printf("⚠️  error incrementando descargas de %s: %v", hashHex, err)
		}
	}

	ipStr := r.Header.Get("X-Forwarded-For")
	if ipStr == "" {
		ipStr, _, _ = net.SplitHostPort(r.RemoteAddr)
	} else {
		ipStr = strings.TrimSpace(strings.Split(ipStr, ",")[0])
	}
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		http.Error(w, "d14:failure reason15:Invalid client", http.StatusBadRequest)
		return
	}

	pMap.Store(peerID, Peer{
		IP:       ip,
		Port:     uint16(port),
		LastSeen: time.Now(),
		Left:     left,
	})

	var compactPeers []byte
	pMap.Range(func(key, value interface{}) bool {
		if key.(string) == peerID {
			return true // no devolvemos al propio peer en su lista de resultados
		}
		p := value.(Peer)
		compactPeers = append(compactPeers, p.IP...)
		pBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(pBytes, p.Port)
		compactPeers = append(compactPeers, pBytes...)
		return true
	})

	w.Header().Set("Content-Type", "text/plain")
	w.Write(encodeTrackerResponse(AnnounceInterval, string(compactPeers)))
}

// =========================================================
// SUBIDA DE TORRENTS (requiere login)
// =========================================================

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	username := getAuthenticatedUser(r)
	if username == "" {
		jsonError(w, "Debes iniciar sesión para subir torrents", http.StatusUnauthorized)
		return
	}

	r.ParseMultipartForm(10 << 20)
	file, header, err := r.FormFile("torrent")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, "Error leyendo archivo", http.StatusInternalServerError)
		return
	}

	decoded, err := bencodeDecode(fileBytes)
	if err != nil {
		jsonError(w, "Archivo .torrent inválido", http.StatusBadRequest)
		return
	}
	torrentMap, ok := decoded.(map[string]interface{})
	if !ok {
		jsonError(w, "Archivo .torrent inválido", http.StatusBadRequest)
		return
	}

	// Añadimos nuestro tracker como prioritario (primer tier), conservando los
	// trackers originales del .torrent como respaldo en tiers siguientes —
	// en vez de borrarlos. Así, si nuestro tracker no responde, el cliente
	// puede caer de vuelta al enjambre original (p.ej. el oficial de una
	// distro Linux), y de paso nunca quedamos aislados de ese enjambre.
	ourTier := []interface{}{TrackerAnnounceURL}
	var combinedTiers []interface{}
	combinedTiers = append(combinedTiers, ourTier)

	if existingList, ok := torrentMap["announce-list"].([]interface{}); ok {
		for _, tier := range existingList {
			combinedTiers = append(combinedTiers, tier)
		}
	} else if existingAnnounce, ok := torrentMap["announce"].(string); ok &&
		existingAnnounce != "" && existingAnnounce != TrackerAnnounceURL {
		combinedTiers = append(combinedTiers, []interface{}{existingAnnounce})
	}

	torrentMap["announce"] = TrackerAnnounceURL // compatibilidad con clientes antiguos que ignoran announce-list
	torrentMap["announce-list"] = combinedTiers

	infoMap, ok := torrentMap["info"]
	if !ok {
		jsonError(w, "Estructura interna corrupta (sin 'info')", http.StatusBadRequest)
		return
	}
	infoBytes, err := bencodeEncode(infoMap)
	if err != nil {
		jsonError(w, "Error calculando info_hash", http.StatusInternalServerError)
		return
	}
	hasher := sha1.New()
	hasher.Write(infoBytes)
	infoHash := fmt.Sprintf("%x", hasher.Sum(nil))

	modifiedBytes, err := bencodeEncode(torrentMap)
	if err != nil {
		jsonError(w, "Error reconstruyendo el torrent", http.StatusInternalServerError)
		return
	}

	exists, err := dbTorrentExists(infoHash)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, "Este torrent ya está indexado", http.StatusConflict)
		return
	}

	meta := TorrentMetadata{
		Name:      header.Filename,
		InfoHash:  infoHash,
		Size:      header.Size,
		Uploaded:  time.Now(),
		Downloads: 0,
		Uploader:  username,
	}
	if err := dbInsertTorrent(meta); err != nil {
		if isUniqueViolation(err) {
			jsonError(w, "Este torrent ya está indexado", http.StatusConflict)
			return
		}
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	safeName := strings.ReplaceAll(header.Filename, "/", "_")
	if err := os.WriteFile(TorrentsDir+"/"+safeName, modifiedBytes, 0644); err != nil {
		jsonError(w, "Error guardando el archivo", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "info_hash": infoHash})
}

// =========================================================
// LISTADO / BÚSQUEDA
// =========================================================

type WebTorrent struct {
	TorrentMetadata
	Seeders  int `json:"seeders"`
	Leechers int `json:"leechers"`
}

func seedersLeechersFor(infoHash string) (int, int) {
	seeders, leechers := 0, 0
	peersMap.Range(func(key, value interface{}) bool {
		if fmt.Sprintf("%x", key.(string)) == infoHash {
			value.(*sync.Map).Range(func(_, v interface{}) bool {
				p := v.(Peer)
				if p.Left == 0 {
					seeders++
				} else {
					leechers++
				}
				return true
			})
		}
		return true
	})
	return seeders, leechers
}

func listTorrentsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	searchQuery := strings.TrimSpace(r.URL.Query().Get("search"))

	snapshot, err := dbListTorrents(searchQuery)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	list := make([]WebTorrent, 0, len(snapshot))
	for _, t := range snapshot {
		seeders, leechers := seedersLeechersFor(t.InfoHash)
		list = append(list, WebTorrent{t, seeders, leechers})
	}

	json.NewEncoder(w).Encode(list)
}

// =========================================================
// ESTADÍSTICAS GLOBALES
// =========================================================

func globalStatsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	activePeers := make(map[string]bool)
	peersMap.Range(func(_, value interface{}) bool {
		value.(*sync.Map).Range(func(k, v interface{}) bool {
			p := v.(Peer)
			activePeers[fmt.Sprintf("%s:%d", p.IP.String(), p.Port)] = true
			return true
		})
		return true
	})

	totalTorrents, _, _, err := dbCountTorrents()
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]int{
		"total_torrents": totalTorrents,
		"active_users":   len(activePeers),
	})
}

// =========================================================
// PANEL DE ADMINISTRACIÓN — USUARIOS
// =========================================================

type AdminUserView struct {
	Username     string `json:"username"`
	CreatedAt    string `json:"created_at"`
	RegisteredIP string `json:"registered_ip"`
	LastLoginIP  string `json:"last_login_ip"`
	LastLoginAt  string `json:"last_login_at"`
	UploadCount  int    `json:"upload_count"`
}

func adminUsersHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	out, err := dbListUsersForAdmin()
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if out == nil {
		out = []AdminUserView{}
	}
	json.NewEncoder(w).Encode(out)
}

// adminCreateUserHandler permite al admin crear una cuenta directamente,
// sin pasar por invitación (alta inmediata, admin decide usuario y contraseña).
func adminCreateUserHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Password) < 6 {
		jsonError(w, "Usuario mín. 3 caracteres, contraseña mín. 6", http.StatusBadRequest)
		return
	}

	exists, err := dbUserExists(req.Username)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, "El usuario ya existe", http.StatusConflict)
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		jsonError(w, "Error interno", http.StatusInternalServerError)
		return
	}

	if err := dbCreateUser(req.Username, hash, "creado por admin"); err != nil {
		if isUniqueViolation(err) {
			jsonError(w, "El usuario ya existe", http.StatusConflict)
			return
		}
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// deleteTorrentFile borra el .torrent del disco de forma segura.
func deleteTorrentFile(fileName string) {
	safeName := strings.ReplaceAll(fileName, "/", "_")
	os.Remove(TorrentsDir + "/" + safeName)
}

// adminDeleteUserHandler borra una cuenta y todos sus torrents (BD + disco).
func adminDeleteUserHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	fileNames, found, err := dbDeleteUserCascade(req.Username)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "Usuario no encontrado", http.StatusNotFound)
		return
	}

	for _, name := range fileNames {
		deleteTorrentFile(name)
	}

	sessionsMutex.Lock()
	for token, s := range sessions {
		if strings.EqualFold(s.Username, req.Username) {
			delete(sessions, token)
		}
	}
	sessionsMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "deleted_torrents": fmt.Sprintf("%d", len(fileNames))})
}

// =========================================================
// PANEL DE ADMINISTRACIÓN — INVITACIONES
// =========================================================

type InviteView struct {
	Code      string `json:"code"`
	CreatedAt string `json:"created_at"`
	UsedBy    string `json:"used_by"`
	UsedAt    string `json:"used_at"`
	Revoked   bool   `json:"revoked"`
}

func adminListInvitesHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	out, err := dbListInvites()
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if out == nil {
		out = []InviteView{}
	}
	json.NewEncoder(w).Encode(out)
}

func adminCreateInviteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	code := generateInviteCode()
	if err := dbCreateInvite(code); err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "code": code})
}

func adminRevokeInviteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	found, err := dbRevokeInvite(req.Code)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "Invitación no encontrada o ya utilizada", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// =========================================================
// PANEL DE ADMINISTRACIÓN — TORRENTS
// =========================================================

type AdminTorrentView struct {
	TorrentMetadata
	Seeders  int `json:"seeders"`
	Leechers int `json:"leechers"`
}

func adminTorrentsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	snapshot, err := dbListTorrents("")
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	out := make([]AdminTorrentView, 0, len(snapshot))
	for _, t := range snapshot {
		seeders, leechers := seedersLeechersFor(t.InfoHash)
		out = append(out, AdminTorrentView{t, seeders, leechers})
	}

	json.NewEncoder(w).Encode(out)
}

func adminDeleteTorrentHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		InfoHash string `json:"info_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	fileName, found, err := dbDeleteTorrent(req.InfoHash)
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}
	if !found {
		jsonError(w, "Torrent no encontrado", http.StatusNotFound)
		return
	}

	deleteTorrentFile(fileName)
	peersMap.Delete(req.InfoHash)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// adminOverviewHandler da un resumen rápido para la cabecera del panel.
func adminOverviewHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	totalUsers, err := dbCountUsers()
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	totalTorrents, totalDownloads, totalSize, err := dbCountTorrents()
	if err != nil {
		jsonError(w, "Error de base de datos", http.StatusInternalServerError)
		return
	}

	activePeers := make(map[string]bool)
	peersMap.Range(func(_, value interface{}) bool {
		value.(*sync.Map).Range(func(_, v interface{}) bool {
			p := v.(Peer)
			activePeers[fmt.Sprintf("%s:%d", p.IP.String(), p.Port)] = true
			return true
		})
		return true
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_users":     totalUsers,
		"total_torrents":  totalTorrents,
		"total_downloads": totalDownloads,
		"total_size":      totalSize,
		"active_peers":    len(activePeers),
	})
}

// =========================================================
// LIMPIEZA DE PEERS EN MEMORIA
// =========================================================

func startJanitor() {
	for range time.Tick(1 * time.Minute) {
		peersMap.Range(func(_, pMapInterface interface{}) bool {
			pMap := pMapInterface.(*sync.Map)
			pMap.Range(func(id, peerInterface interface{}) bool {
				if time.Since(peerInterface.(Peer).LastSeen) > PeerExpiryTime {
					pMap.Delete(id)
				}
				return true
			})
			return true
		})
	}
}
