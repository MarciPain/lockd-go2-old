# lockd2 – Használati útmutató

`lockd2` egy MQTT–HTTP gateway okoszárakhoz. MQTT brokeren figyeli a zárak
állapotát és akkumulátorszintjét, és egy hitelesített HTTPS REST API-n keresztül
teszi lehetővé a zárak vezérlését.

---

## Tartalom

1. [Gyors start](#gyors-start)
2. [Fordítás](#fordítás)
3. [Parancssori kapcsolók](#parancssori-kapcsolók)
4. [Konfiguráció](#konfiguráció)
5. [Hitelesítés – API kulcsok](#hitelesítés--api-kulcsok)
6. [ACL – hozzáférés-vezérlés](#acl--hozzáférés-vezérlés)
7. [REST API referencia](#rest-api-referencia)
8. [Audit napló](#audit-napló)
9. [Konfig és tanúsítvány újratöltés (SIGHUP)](#konfig-és-tanúsítvány-újratöltés-sighup)
10. [Systemd telepítés](#systemd-telepítés)

---

## Gyors start

```bash
# 1. Fordítás
go build -o lockd2 .

# 2. Konfig fájl létrehozása
cp doc/config.example.json /etc/lockd2/lockd2.json
# szerkeszd meg a saját adataiddal

# 3. Auth kulcs generálása
./lockd2 -gen-key alice
# kimenet: Raw Key (mobil appba) + auth_keys sor (fájlba másolandó)

# 4. Indítás
./lockd2 -config /etc/lockd2/lockd2.json
```

---

## Fordítás

```bash
go build -o lockd2 .
```

Go 1.18 vagy újabb szükséges. Függőségek (`go.sum`-ban rögzítve):
- `github.com/eclipse/paho.mqtt.golang`
- `github.com/gorilla/websocket` (tranzitív)
- `golang.org/x/net`, `golang.org/x/sync` (tranzitív)

---

## Parancssori kapcsolók

| Kapcsoló | Alapértelmezett | Leírás |
|---|---|---|
| `-config <fájl>` | `/etc/lockd2/lockd2.json` | JSON konfig fájl elérési útja |
| `-encode <szöveg>` | – | Base64-kódolja a megadott stringet (MQTT jelszóhoz) |
| `-gen-key <username>` | – | API kulcsot generál a megadott felhasználóhoz |
| `-testauth <fájl>` | – | Ellenőrzi, hogy a `<username>.json` tokene szerepel-e az auth_keys-ben |
| `-set-admin-key <kulcs>` | – | Admin kulcs hash-ét beírja az `admin_key_file`-ba |
| `-discover` | – | MQTT-re csatlakozik és kiírja az összes beérkező üzenetet |

### `-encode` – MQTT hitelesítő adatok kódolása

Az MQTT `username` és `password` mező Base64-kódolt formában kerül a configba.

```bash
./lockd2 -encode "mqtt-felhasznalo"
./lockd2 -encode "titkos-jelszo"
```

A kimenetet másold a config `mqtt.username` / `mqtt.password` mezőjébe.

### `-gen-key` – API kulcs generálása

```bash
./lockd2 -gen-key alice
```

Az URL-t a config `http.external_url` mezőjéből veszi. A parancs automatikusan:
1. **Hozzáfűzi** a hash-t az `auth_keys` fájlhoz (a configból veszi az útvonalat).
2. **Létrehozza** az `alice.json` kliens konfig fájlt az aktuális könyvtárban:

```json
{
  "url": "http://lockd.reas.hu:8089",
  "token": "1748123456789-a3f9..."
}
```

Kimenet példa:

```
--- NEW API KEY GENERATED ---
User:          alice
Raw Key:       1748123456789-a3f9...
Auth keys:     /etc/lockd2/auth_keys  <-- sor hozzáfűzve
Client config: alice.json             <-- add át a kliensnek
-----------------------------
```

Ezt a JSON-t add át a kliensnek / töltsd fel a mobil appba.

### `-testauth` – token ellenőrzése

Ellenőrzi, hogy a generált kliens JSON-ban lévő token hash-e megtalálható-e az `auth_keys` fájlban – pontosan ugyanúgy, ahogy a szerver hitelesít.

```bash
./lockd2 -testauth alice.json
```

Sikeres kimenet:
```
OK: a token érvényes, felhasználó: alice
```

Hibás kimenet (pl. az auth_keys-ből törölték a sort):
```
HIBA: a token hash (...) nem szerepel az auth_keys-ben (/etc/lockd2/auth_keys)
```

### `-set-admin-key` – Admin kulcs beállítása

Az admin kulcsot hash-elve tárolja az `admin_key_file`-ban (a configból veszi az útvonalat). Csak egyszer kell lefuttatni, vagy ha kulcsot cserélsz.

```bash
./lockd2 -set-admin-key "titkos-admin-jelszo"
```

A nyers kulcsot soha nem tárolja, csak a SHA256 hash-ét. Utána a webes admin felületen ezt a kulcsot kell megadni.

### `-discover` – MQTT topicok felfedezése

Csatlakozik a brokerhez (a config alapján) és kiírja az összes beérkező üzenetet. Hasznos a zár ID-k és topic struktúra meghatározásához a konfig fájl kitöltése előtt.

```bash
./lockd2 -discover
```

Ctrl+C-vel lehet leállítani.

---

## Konfiguráció

A konfig fájl JSON formátumú. Alapértelmezett helye: `/etc/lockd2/lockd2.json`.
Lásd a `doc/config.example.json` fájlt teljes példáért.

### Kulcsmezők

#### `mqtt`

| Mező | Típus | Leírás |
|---|---|---|
| `broker` | string | MQTT broker hostname |
| `port` | int | MQTT port (TLS esetén általában 8883) |
| `username` | string | **Base64-kódolt** MQTT felhasználónév |
| `password` | string | **Base64-kódolt** MQTT jelszó |
| `ca_file` | string | CA tanúsítvány fájl a broker TLS ellenőrzéséhez |
| `client_id` | string | MQTT kliens azonosító |
| `topic_state` | string | Állapot topic (pl. `locks/+/state`) |
| `topic_batt` | string | Akkumulátor topic (pl. `locks/+/batt`) |
| `topic_cmd_tpl` | string | Parancs topic template (pl. `locks/%s/cmd`) |

A `topic_state` és `topic_batt` mezőkben a `+` wildcard bármely zár ID-ra illeszkedik.
A `topic_cmd_tpl`-ben a `%s` helyére a zár ID kerül.

#### `http`

| Mező | Alapértelmezett | Leírás |
|---|---|---|
| `listen` | `127.0.0.1:8884` | Figyelési cím és port |
| `external_url` | – | Publikus URL (bekerül a `-gen-key` által generált kliens JSON-ba) |
| `auth_file` | `/etc/lockd2/auth_keys` | API kulcsok fájlja |
| `audit_file` | `/etc/lockd2/audit.log` | Audit napló fájl |
| `cert_file` | – | TLS tanúsítvány (PEM). Ha üres: plain HTTP |
| `key_file` | – | TLS privát kulcs (PEM) |
| `admin_key_file` | – | Admin kulcs hash fájlja (lásd `-set-admin-key`) |

Ha `cert_file` és `key_file` meg van adva, a szerver HTTPS-en indul.

#### `locks` – zárak listája

```json
{
  "id": "frontdoor",
  "name": "Bejárati ajtó",
  "type": "TOGGLE",
  "has_battery": true
}
```

| Mező | Leírás |
|---|---|
| `id` | Egyedi azonosító (MQTT topic-ban is szerepel) |
| `name` | Megjelenítési név |
| `type` | `TOGGLE` / `STRIKE` / `PULSE` |
| `has_battery` | `true` ha az eszköz jelez akkumulátorszintet |

**Zár típusok:**
- `TOGGLE` – hagyományos retesz (támogatja: `OPEN`, `LOCK`)
- `STRIKE` – elektromos zárpenge (csak `OPEN`)
- `PULSE` – impulzusvezérelt (csak `OPEN`)

#### `acl` – hozzáférés-vezérlés

```json
[
  { "user": "alice", "locks": ["frontdoor", "garage"] },
  { "user": "*",     "locks": ["lobby"] }
]
```

Ha az `acl` tömb üres, minden hitelesített felhasználó minden zárat elér.

---

## Hitelesítés – API kulcsok

Az API kulcsos hitelesítés az `X-API-Key` HTTP fejléccel vagy a `key` URL
paraméterrel működik.

```
X-API-Key: <raw_key>
# vagy
GET /v1/locks?key=<raw_key>
```

Az `auth_file` formátuma (`<felhasználónév>:<sha256_hash>`):

```
# lockd2 auth keys
alice:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
bob:a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3
```

A hash-t a `-gen-key` kapcsolóval generálhatod. Soha ne a nyers kulcsot tárold.

---

## ACL – hozzáférés-vezérlés

Az ACL szabályok felülről lefelé értékelődnek. Az első illeszkedő szabály érvényes.

- `"user": "*"` – minden hitelesített felhasználóra vonatkozik
- `"locks": ["*"]` – minden zárhoz hozzáférést ad

Ha nincs ACL konfiguráció, minden hitelesített user minden zárat elér.

---

## REST API referencia

Az API base URL-je: `https://<host>:<port>`

### `GET /healthz`

Hitelesítés nélkül elérhető. Visszaadja, hogy az MQTT kapcsolat él-e.

```
200 OK       → "ok\n"
503          → "mqtt disconnected\n"
```

### `GET /v1/locks`

Az összes, a felhasználó számára elérhető zár listája állapottal.

**Kérés:**
```bash
curl -H "X-API-Key: <key>" https://localhost:8884/v1/locks
```

**Válasz:**
```json
{
  "locks": [
    {
      "id": "frontdoor",
      "name": "Bejárati ajtó",
      "type": "TOGGLE",
      "has_battery": true,
      "state": "Zárva",
      "battery": "87%",
      "updated_at": "2026-05-31T10:23:00Z"
    }
  ]
}
```

### `GET /v1/locks/{id}`

Egy zár aktuális állapota.

```bash
curl -H "X-API-Key: <key>" https://localhost:8884/v1/locks/frontdoor
```

**Válasz:**
```json
{
  "lock_id": "frontdoor",
  "state": "Zárva",
  "battery": "87%",
  "updated_at": "2026-05-31T10:23:00Z"
}
```

Hibakódok: `403 Forbidden` (nincs ACL jog), `404 Not Found` (ismeretlen ID).

### `POST /v1/locks/{id}/cmd`

Parancs küldése egy zárnak.

```bash
curl -X POST \
     -H "X-API-Key: <key>" \
     -H "Content-Type: application/json" \
     -d '{"cmd": "OPEN"}' \
     https://localhost:8884/v1/locks/frontdoor/cmd
```

**Érvényes parancsok:**

| Parancs | TOGGLE | STRIKE | PULSE |
|---|---|---|---|
| `OPEN` | ✓ | ✓ | ✓ |
| `LOCK` | ✓ | ✗ | ✗ |

**Sikeres válasz:**
```json
{ "ok": true }
```

**Hibakódok:**
| Kód | Leírás |
|---|---|
| `400` | Érvénytelen JSON vagy nem támogatott parancs |
| `403` | Nincs ACL jog |
| `404` | Ismeretlen zár ID |
| `405` | Nem POST kérés |
| `500` | MQTT publish hiba |

---

## Audit napló

Minden sikeres parancsküldést a `http.audit_file`-ba naplóz a szerver:

```
2026-05-31 10:23:00 | User: alice | Lock: frontdoor | Cmd: OPEN
2026-05-31 10:25:14 | User: bob   | Lock: garage    | Cmd: LOCK
```

A fájl szülőkönyvtárát a szerver automatikusan létrehozza induláskor.

---

## Konfig és tanúsítvány újratöltés (SIGHUP)

A futó folyamatnak `SIGHUP` jelet küldve újratölti a konfig fájlt és a TLS
tanúsítványt – szerver újraindítás nélkül:

```bash
kill -HUP $(pidof lockd2)
# vagy systemd esetén:
systemctl reload lockd2
```

Hasznos Let's Encrypt certbot megújítás után (`--deploy-hook`).

---

## Systemd telepítés

Részletes leírás és a unit fájlok: `doc/lockd2.service` és `doc/lockd2-cert-reload.service`.

Gyors telepítés:

```bash
# Bináris másolása
install -o root -g root -m 0755 lockd2 /usr/local/bin/lockd2

# Konfig másolása
install -o root -g root -m 0640 doc/config.example.json /etc/lockd2/lockd2.json
# Szerkeszd meg: $EDITOR /etc/lockd2/lockd2.json

# Systemd unit
install -o root -g root -m 0644 doc/lockd2.service /etc/systemd/system/lockd2.service
systemctl daemon-reload
systemctl enable --now lockd2
```

---

## Mobil alkalmazás (lockd2-app)

### Profilok

Az app több szervert is kezelni tud profil formájában. Minden profilhoz
tartozik egy szerver URL és egy API kulcs.

- **1 profil esetén** a profil-váltó nem jelenik meg, az app egyszerűen
  használja az egyetlen konfigurált profilt.
- **2+ profil esetén** az AppBar-ban megjelenik az aktív profil neve
  (lenyíló nyíllal), és a profil-kezelő ikonra kattintva lehet profilok
  között váltani, újat hozzáadni vagy törölni.

Új profil felvétele: beállítás ikon → „Betöltés fájlból" (a `-gen-key`
által generált JSON), majd a profil nevének megadása és MENTÉS.

### Android home screen widget

Az alkalmazás Android home screen widgetet biztosít, amely az aktív profil
zárait és azok utolsó ismert állapotát jeleníti meg.

**Fontos tudnivalók:**

- A widget az **utolsó sikeres poll** állapotát mutatja – azt, amikor az app
  utoljára előtérben volt és sikeresen lekérdezte a szervert.
- A widget az **aktív profil** zárait mutatja (amelyiken éppen állsz az appban,
  amikor a widget utoljára frissült).
- Ha bezárod az appot, a widget **az utolsó ismert állapotot tartja** egészen
  addig, amíg újra meg nem nyitod az alkalmazást.
- Háttérbeli automatikus frissítés jelenleg nem elérhető – a push értesítésekhez
  használd az `ntfy_url` funkciót (ld. lentebb).

### Push értesítések (ntfy.sh)

A szerver képes push értesítést küldeni, ha egy zár állapota megváltozik.
Ehhez add hozzá a `lockd2.json` konfighoz:

```json
"http": {
  ...
  "ntfy_url": "https://ntfy.sh/sajat-egyedi-tema"
}
```

Majd telepítsd az [ntfy](https://ntfy.sh) alkalmazást a telefonodra, és
iratkozz fel ugyanerre a témára. Az értesítés formátuma:

```
Bejárati ajtó: UNLOCK
```

Az `ntfy_url` bármilyen HTTP POST-ot fogadó webhook URL-re mutathat
(Home Assistant, Node-RED, egyéni script stb.), nem csak az ntfy.sh-ra.
A POST body mindig `<zár neve>: <állapot>` formátumú szöveges üzenet.
```
