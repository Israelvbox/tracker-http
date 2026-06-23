# Tracker-http

Un tracker BitTorrent HTTP completo (protocolo, bencode y formato compacto de
peers implementados desde cero en Go, sin librerías de BitTorrent de
terceros) junto con una web mínima para indexar y descargar torrents:
registro/login, subida con reescritura automática del `announce`, búsqueda,
ranking de descargas, sistema de reportes/moderación y panel de
administración.

Pensado como punto de partida genérico — adáptalo al contenido que quieras
distribuir (siempre que tengas derecho a hacerlo).

## Instalación

Requisitos: una VM/servidor con Debian o Ubuntu, acceso root, y este
proyecto descomprimido en cualquier carpeta.

```bash
chmod +x install.sh
sudo ./install.sh
```

El instalador te preguntará:
- **Dominio** público de tu tracker (ej. `tracker.ejemplo.com`)
- **Nombre del sitio** a mostrar en la web
- **Carpeta de instalación** (por defecto `/var/www/tracker`)
- **Nombre y usuario de la base de datos** Postgres
- **Contraseña de la base de datos** (puedes dejarla en blanco para que se
  autogenere una segura, sin caracteres especiales)

El `ADMIN_TOKEN` del panel de administración **siempre** se autogenera, no
se pregunta.

### Instalación no interactiva

Si prefieres pasar los valores por argumento (útil para scripts/CI):

```bash
sudo ./install.sh --domain=tracker.ejemplo.com --site-name="Mi Tracker" \
  --install-dir=/var/www/tracker --db-password=unaContraseñaSegura123
```

## Qué instala y configura el script

- **Go 1.22.5** (versión oficial descargada de go.dev, no la de `apt`)
- **PostgreSQL** — crea la base de datos y el usuario, y arregla los
  permisos del esquema `public` (necesario en Postgres 15+)
- **Nginx** — sirve los archivos estáticos y hace proxy de `/api/*` y
  `/announce` al backend Go; incluye gzip y cache de estáticos
- **Servicio systemd** (`tracker.service`) con `ADMIN_TOKEN` y
  `DATABASE_URL` ya configurados
- Compila el binario y sustituye los placeholders `__DOMAIN__` y
  `__SITE_NAME__` en el código y en la web por los valores que indicaste

## Después de instalar

1. **Si usas Caddy (u otro proxy) delante para HTTPS**, apunta su
   `reverse_proxy` al **puerto 80** de esta máquina (Nginx), no al 8080
   (Go) directamente. Ejemplo de Caddyfile:
   ```
   tracker.ejemplo.com {
       reverse_proxy IP_DE_ESTA_VM:80
   }
   ```

2. **Accede al panel admin** en `https://tudominio/admin.html` con el
   `ADMIN_TOKEN` que te dio el instalador al final.

3. **Revisa el texto legal/de normas de uso** en `public/upload.html` —
   está pensado para que lo adaptes al tipo de contenido que vas a permitir
   en tu instancia.

## Volver a ejecutar el instalador

Es seguro volver a ejecutarlo: si la base de datos ya existe, no la borra
(solo avisa). El servicio systemd y la configuración de Nginx sí se
regeneran cada vez con los valores que introduzcas. Para solo redesplegar
código sin pasar por todo el instalador:

```bash
cd /var/www/tracker   # o la carpeta que elegiste
go build -o http-tracker .
sudo systemctl restart tracker
```

## Estructura del proyecto

```
main.go, db.go, reports.go, bencode.go, password.go   → backend Go
schema.sql                                              → referencia del esquema SQL
public/                                                 → frontend (HTML/CSS/JS)
install.sh                                              → instalador interactivo
```

## Comandos de diagnóstico

```bash
systemctl status tracker
journalctl -u tracker -f
sudo -u postgres psql -d tracker -c "SELECT * FROM torrents;"
curl -I http://localhost/
```

## Notas de diseño / decisiones técnicas

- **Bencode propio** (`bencode.go`): evita depender de paquetes pesados de
  terceros solo para codificar/decodificar el formato.
- **Hashing de contraseñas propio** (`password.go`, PBKDF2-SHA256 con la
  librería estándar): evita depender de `golang.org/x/crypto` en entornos
  con acceso de red restringido a proxies de módulos Go.
- **`AnnounceInterval` y `PeerExpiryTime`**: el segundo debe ser siempre
  mayor que el primero (con margen). Si los cambias, mantén esa relación o
  los peers "desaparecerán" del listado entre anuncios aunque sigan activos.
- **Sesiones en memoria**: se pierden al reiniciar el servicio (los
  usuarios tendrán que volver a iniciar sesión). Los datos persistentes
  (usuarios, torrents, reportes) están en Postgres.

  ## Preview
  **Pagina de inicio**
  <img src="https://github.com/Israelvbox/tracker-http/blob/main/images/main.png?raw=true">

  **Panel de administrador**
  <img src="https://github.com/Israelvbox/tracker-http/blob/main/images/admin.png?raw=true">
