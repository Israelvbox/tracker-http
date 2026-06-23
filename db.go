package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

// connectDB abre la conexión a Postgres usando DATABASE_URL y crea las tablas
// si no existen. Si DATABASE_URL no está definida, el programa no arranca:
func connectDB() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("❌ Falta la variable de entorno DATABASE_URL. " +
			"Ejemplo: postgres://tracker_user:contraseña@localhost:5432/tracker?sslmode=disable")
	}

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("❌ No se pudo abrir la conexión a Postgres: %v", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Reintenta el ping unos segundos, por si Postgres tarda en arrancar
	// (útil si systemd lanza ambos servicios casi a la vez).
	var pingErr error
	for i := 0; i < 10; i++ {
		pingErr = db.Ping()
		if pingErr == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if pingErr != nil {
		log.Fatalf("❌ No se pudo conectar a Postgres tras varios intentos: %v", pingErr)
	}

	if err := runMigrations(); err != nil {
		log.Fatalf("❌ Error creando el esquema de la base de datos: %v", err)
	}

	fmt.Println("🗄️  Conectado a Postgres y esquema verificado.")
}

func runMigrations() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		username        TEXT PRIMARY KEY,
		password_hash   TEXT NOT NULL,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		registered_ip   TEXT,
		last_login_ip   TEXT,
		last_login_at   TIMESTAMPTZ,
		banned          BOOLEAN NOT NULL DEFAULT false
	);

	CREATE TABLE IF NOT EXISTS torrents (
		info_hash       TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		size            BIGINT NOT NULL DEFAULT 0,
		uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		downloads       INTEGER NOT NULL DEFAULT 0,
		uploader        TEXT REFERENCES users(username) ON DELETE SET NULL
	);

	CREATE TABLE IF NOT EXISTS reports (
		id              TEXT PRIMARY KEY,
		info_hash       TEXT NOT NULL,
		torrent_name    TEXT NOT NULL,
		reason          TEXT NOT NULL,
		details         TEXT,
		reporter_ip     TEXT NOT NULL,
		username        TEXT,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		resolved        BOOLEAN NOT NULL DEFAULT false
	);

	CREATE INDEX IF NOT EXISTS idx_torrents_name_lower ON torrents (LOWER(name));
	CREATE INDEX IF NOT EXISTS idx_torrents_uploader ON torrents (uploader);
	CREATE INDEX IF NOT EXISTS idx_reports_resolved ON reports (resolved);
	CREATE INDEX IF NOT EXISTS idx_reports_info_hash ON reports (info_hash);
	`
	_, err := db.Exec(schema)
	return err
}

// =========================================================
// USUARIOS
// =========================================================

func dbCreateUser(username, passwordHash, registeredIP string) error {
	_, err := db.Exec(
		`INSERT INTO users (username, password_hash, registered_ip) VALUES ($1, $2, $3)`,
		username, passwordHash, registeredIP,
	)
	return err
}

// dbGetUserByUsername hace la búsqueda insensible a mayúsculas (igual que antes con EqualFold).
func dbGetUserByUsername(username string) (*User, error) {
	row := db.QueryRow(
		`SELECT username, password_hash, created_at, registered_ip,
		        COALESCE(last_login_ip, ''), last_login_at, banned
		 FROM users WHERE LOWER(username) = LOWER($1)`,
		username,
	)
	var u User
	var lastLoginAt sql.NullTime
	err := row.Scan(&u.Username, &u.PasswordHash, &u.CreatedAt, &u.RegisteredIP, &u.LastLoginIP, &lastLoginAt, &u.Banned)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastLoginAt.Valid {
		u.LastLoginAt = lastLoginAt.Time
	}
	return &u, nil
}

func dbUserExists(username string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(username) = LOWER($1))`, username).Scan(&exists)
	return exists, err
}

func dbUpdateLastLogin(username, ip string) error {
	_, err := db.Exec(
		`UPDATE users SET last_login_ip = $1, last_login_at = now() WHERE LOWER(username) = LOWER($2)`,
		ip, username,
	)
	return err
}

func dbSetUserBanned(username string, banned bool) (bool, error) {
	res, err := db.Exec(`UPDATE users SET banned = $1 WHERE LOWER(username) = LOWER($2)`, banned, username)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbIsUserBanned(username string) (bool, error) {
	var banned bool
	err := db.QueryRow(`SELECT banned FROM users WHERE LOWER(username) = LOWER($1)`, username).Scan(&banned)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return banned, err
}

// AdminUserView ya está definido en main.go; aquí solo lo poblamos.
func dbListUsersForAdmin() ([]AdminUserView, error) {
	rows, err := db.Query(`
		SELECT u.username, u.created_at, COALESCE(u.registered_ip, ''),
		       COALESCE(u.last_login_ip, ''), u.last_login_at, u.banned,
		       COUNT(t.info_hash) AS upload_count
		FROM users u
		LEFT JOIN torrents t ON t.uploader = u.username
		GROUP BY u.username, u.created_at, u.registered_ip, u.last_login_ip, u.last_login_at, u.banned
		ORDER BY u.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminUserView
	for rows.Next() {
		var v AdminUserView
		var createdAt time.Time
		var lastLoginAt sql.NullTime
		if err := rows.Scan(&v.Username, &createdAt, &v.RegisteredIP, &v.LastLoginIP, &lastLoginAt, &v.Banned, &v.UploadCount); err != nil {
			return nil, err
		}
		v.CreatedAt = createdAt.Format(time.RFC3339)
		if lastLoginAt.Valid {
			v.LastLoginAt = lastLoginAt.Time.Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dbCountUsers() (total int, banned int, err error) {
	err = db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE banned) FROM users`).Scan(&total, &banned)
	return
}

// dbDeleteUserCascade borra al usuario Y todos sus torrents (la base de datos),
// en una sola transacción. Devuelve los nombres de archivo de los torrents
// borrados para que el caller los elimine también del disco.
// found=false si el usuario no existía.
func dbDeleteUserCascade(username string) (deletedFileNames []string, found bool, err error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	// 1) Recoger los nombres de archivo de sus torrents (para borrarlos del disco después)
	rows, err := tx.Query(`SELECT name FROM torrents WHERE uploader = $1`, username)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, false, err
		}
		deletedFileNames = append(deletedFileNames, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	// 2) Borrar sus torrents (esto también limpia los reportes huérfanos por FK... pero
	//    reports.info_hash no tiene FK, así que los dejamos como rastro histórico).
	if _, err := tx.Exec(`DELETE FROM torrents WHERE uploader = $1`, username); err != nil {
		return nil, false, err
	}

	// 3) Borrar al usuario. RowsAffected nos dice si existía.
	res, err := tx.Exec(`DELETE FROM users WHERE LOWER(username) = LOWER($1)`, username)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, false, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return deletedFileNames, true, nil
}

// =========================================================
// TORRENTS
// =========================================================

func dbInsertTorrent(t TorrentMetadata) error {
	_, err := db.Exec(
		`INSERT INTO torrents (info_hash, name, size, uploaded_at, downloads, uploader)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.InfoHash, t.Name, t.Size, t.Uploaded, t.Downloads, t.Uploader,
	)
	return err
}

func dbTorrentExists(infoHash string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM torrents WHERE info_hash = $1)`, infoHash).Scan(&exists)
	return exists, err
}

func dbGetTorrentByHash(infoHash string) (*TorrentMetadata, error) {
	row := db.QueryRow(
		`SELECT info_hash, name, size, uploaded_at, downloads, COALESCE(uploader, '')
		 FROM torrents WHERE info_hash = $1`,
		infoHash,
	)
	var t TorrentMetadata
	err := row.Scan(&t.InfoHash, &t.Name, &t.Size, &t.Uploaded, &t.Downloads, &t.Uploader)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// dbListTorrents devuelve los torrents filtrados por búsqueda (en el nombre) y
// ordenados por descargas ("top") o por fecha de subida ("recent", por defecto).
func dbListTorrents(search, sortType string) ([]TorrentMetadata, error) {
	query := `
		SELECT info_hash, name, size, uploaded_at, downloads, COALESCE(uploader, '')
		FROM torrents
	`
	args := []interface{}{}
	if search != "" {
		query += ` WHERE name ILIKE $1`
		args = append(args, "%"+search+"%")
	}
	if sortType == "top" {
		query += ` ORDER BY downloads DESC`
	} else {
		query += ` ORDER BY uploaded_at DESC`
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TorrentMetadata
	for rows.Next() {
		var t TorrentMetadata
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.Size, &t.Uploaded, &t.Downloads, &t.Uploader); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func dbIncrementDownloads(infoHash string) error {
	_, err := db.Exec(`UPDATE torrents SET downloads = downloads + 1 WHERE info_hash = $1`, infoHash)
	return err
}

func dbDeleteTorrent(infoHash string) (name string, found bool, err error) {
	row := db.QueryRow(`DELETE FROM torrents WHERE info_hash = $1 RETURNING name`, infoHash)
	err = row.Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

func dbCountTorrents() (total int, totalDownloads int, totalSize int64, err error) {
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(downloads), 0), COALESCE(SUM(size), 0) FROM torrents`).
		Scan(&total, &totalDownloads, &totalSize)
	return
}

// =========================================================
// REPORTES
// =========================================================

func dbInsertReport(rep Report) error {
	_, err := db.Exec(
		`INSERT INTO reports (id, info_hash, torrent_name, reason, details, reporter_ip, username, created_at, resolved)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		rep.ID, rep.InfoHash, rep.TorrentName, rep.Reason, rep.Details, rep.ReporterIP, rep.Username, rep.CreatedAt, rep.Resolved,
	)
	return err
}

func dbListReports(resolved bool) ([]Report, error) {
	rows, err := db.Query(
		`SELECT id, info_hash, torrent_name, reason, COALESCE(details, ''), reporter_ip,
		        COALESCE(username, ''), created_at, resolved
		 FROM reports WHERE resolved = $1 ORDER BY created_at DESC`,
		resolved,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.InfoHash, &r.TorrentName, &r.Reason, &r.Details, &r.ReporterIP, &r.Username, &r.CreatedAt, &r.Resolved); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbResolveReport(id string, resolved bool) (bool, error) {
	res, err := db.Exec(`UPDATE reports SET resolved = $1 WHERE id = $2`, resolved, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// dbReportCountsByHash devuelve cuántos reportes SIN RESOLVER tiene cada info_hash.
func dbReportCountsByHash() (map[string]int, error) {
	rows, err := db.Query(`SELECT info_hash, COUNT(*) FROM reports WHERE resolved = false GROUP BY info_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var hash string
		var count int
		if err := rows.Scan(&hash, &count); err != nil {
			return nil, err
		}
		counts[hash] = count
	}
	return counts, rows.Err()
}

// isUniqueViolation detecta el error de Postgres de clave duplicada (código 23505),
// sin necesidad de importar el paquete de tipos de error de lib/pq.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key value violates unique constraint")
}
