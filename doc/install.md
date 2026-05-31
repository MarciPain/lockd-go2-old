# lockd2 – Telepítési útmutató

## 1. Rendszerfeltételek

- Linux (systemd)
- Go 1.24+ (csak fordításhoz)
- Futó MQTT broker (TLS-sel, pl. Mosquitto)
- Opcionálisan: Let's Encrypt / saját CA a HTTP TLS-hez

### Go telepítése Ubuntu 24.04-en

Az Ubuntu 24.04 alapból Go 1.22-t tartalmaz, ami nem elég újabb (`paho.mqtt.golang v1.5+` megköveteli az 1.24-et). A legegyszerűbb megoldás snap-pel:

```bash
snap install go --classic
```

---

## 2. Fordítás és bináris telepítése

```bash
cd /opt/lockd2
go build -o lockd2 .

install -o root -g root -m 0755 lockd2 /usr/local/bin/lockd2
```

---

## 3. Rendszerfelhasználó és könyvtárak létrehozása

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin lockd2

mkdir -p /etc/lockd2/tls
chown root:lockd2 /etc/lockd2
chmod 750 /etc/lockd2

# Auth kulcsok fájlja – lockd2 írja (kulcsgenerálás), root olvassa
touch /etc/lockd2/auth_keys
chown lockd2:lockd2 /etc/lockd2/auth_keys
chmod 600 /etc/lockd2/auth_keys

# Audit napló fájl
touch /etc/lockd2/audit.log
chown lockd2:lockd2 /etc/lockd2/audit.log
chmod 600 /etc/lockd2/audit.log
```

---

## 4. Konfig fájl

```bash
cp doc/config.example.json /etc/lockd2/lockd2.json
chown root:lockd2 /etc/lockd2/lockd2.json
chmod 640 /etc/lockd2/lockd2.json

# Szerkeszd meg a saját értékeiddel:
$EDITOR /etc/lockd2/lockd2.json
```

**MQTT jelszó Base64 kódolása:**
```bash
/usr/local/bin/lockd2 -encode "mqtt-jelszo"
```
A kimenetet másold a config `mqtt.password` mezőjébe.

---

## 5. Admin kulcs beállítása

Az admin kulcs hash-elve tárolódik, a nyers kulcsot a szerver soha nem látja fájlban.

```bash
# Admin kulcs fájl létrehozása (csak root olvashatja)
touch /etc/lockd2/admin_key
chown root:lockd2 /etc/lockd2/admin_key
chmod 640 /etc/lockd2/admin_key

# Kulcs beállítása
/usr/local/bin/lockd2 -set-admin-key "titkos-admin-jelszo"
```

A configban meg kell adni a fájl útvonalát:
```json
"admin_key_file": "/etc/lockd2/admin_key"
```

Ezután a webes admin felület elérhető: `https://<host>:<port>/admin`

## 6. API kulcs generálása

```bash
/usr/local/bin/lockd2 -gen-key alice
```

Az URL-t a config `http.external_url` mezőjéből veszi. A parancs automatikusan:
- hozzáfűzi a hash-t az `/etc/lockd2/auth_keys` fájlhoz
- létrehozza az `alice.json` kliens konfig fájlt az aktuális könyvtárban

```json
{
  "url": "http://lockd.reas.hu:8089",
  "token": "<raw_key>"
}
```

Ezt a JSON-t add át a kliensnek.

---

## 7. TLS tanúsítvány (opcionális, ajánlott)

### Let's Encrypt (certbot)

```bash
certbot certonly --standalone -d lock.example.com

# Szimlinks másolás a lockd2 könyvtárba
ln -s /etc/letsencrypt/live/lock.example.com/fullchain.pem /etc/lockd2/tls/server.crt
ln -s /etc/letsencrypt/live/lock.example.com/privkey.pem   /etc/lockd2/tls/server.key
chown -h lockd2:lockd2 /etc/lockd2/tls/server.crt /etc/lockd2/tls/server.key
```

### Automatikus újratöltés certbot megújításkor

Telepítsd a `.path` és `.service` unit-okat, amelyek figyelik a cert fájlt, és
SIGHUP-pal újratöltik a lockd2-t – szerver újraindítás nélkül:

```bash
install -m 0644 doc/lockd2-cert-reload.service /etc/systemd/system/
install -m 0644 doc/lockd2-cert-reload.path    /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now lockd2-cert-reload.path
```

---

## 8. Systemd service telepítése

```bash
install -m 0644 doc/lockd2.service /etc/systemd/system/lockd2.service
systemctl daemon-reload
systemctl enable --now lockd2
```

Állapot ellenőrzés:

```bash
systemctl status lockd2
journalctl -u lockd2 -f
```

---

## 9. Tűzfal (opcionális)

Ha a szerver kívülről is elérhető kell legyen:

```bash
# UFW
ufw allow 8884/tcp comment "lockd2"

# firewalld
firewall-cmd --permanent --add-port=8884/tcp
firewall-cmd --reload
```

---

## 10. Gyors teszt

```bash
# Health check (auth nélkül)
curl https://localhost:8884/healthz --cacert /etc/lockd2/tls/server.crt

# Zárak listája
curl -H "X-API-Key: <raw_key>" https://localhost:8884/v1/locks --cacert /etc/lockd2/tls/server.crt

# Parancs küldése
curl -X POST \
     -H "X-API-Key: <raw_key>" \
     -H "Content-Type: application/json" \
     -d '{"cmd":"OPEN"}' \
     https://localhost:8884/v1/locks/frontdoor/cmd \
     --cacert /etc/lockd2/tls/server.crt
```

---

## 11. Frissítés

```bash
cd /opt/lockd2
git pull
go build -o lockd2 .
install -o root -g root -m 0755 lockd2 /usr/local/bin/lockd2
systemctl restart lockd2
```

Konfig változás esetén újraindítás helyett elég az újratöltés:

```bash
systemctl reload lockd2
```
