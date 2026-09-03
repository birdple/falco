# falco — procesamiento y entrega de imágenes

Recibe imágenes, las reencoda con libvips y las sirve transformadas al vuelo.
No guarda bytes: el almacenamiento se delega a jay por su protocolo TCP nativo.
Además es un proyecto open source público, así que hay código que birdple-v2 no
usa y que no se borra.

## Stack

- Go 1.27, chi v5, zerolog.
- libvips vía `github.com/cshum/vipsgen` (cgo). No es govips.
- Config: defaults en código + viper sobre `config.yaml` + godotenv + env vars.
- Cache de imágenes transformadas: LRU sharded en proceso; Redis opcional.
- Storage: `github.com/ivangsm/jay/proto/client`.
- Panel admin: templ, con los assets embebidos en `web/`.
- Prometheus (`/metrics`) + OTel (`internal/telemetry`); `gobreaker` envuelve el
  backend de storage.

## Arrancar, probar, revisar

```bash
make check            # gate de commit: fmt + vet + lint + test + go build ./...
make test             # go test ./...
make lint             # golangci-lint, config en .golangci.yml
go run ./cmd/server   # exige libvips en el host
```

libvips es un requisito del host, no algo opcional: sin él ni siquiera compila
(`brew install vips` en macOS, `apt install libvips-dev` en Linux). El
Dockerfile ya lo trae.

No uses `make build`: cross-compila a Linux con CGO y no corre en macOS. Para
compilar todo en el host está `make check-build`.

En el stack completo: `docker compose up -d falco` desde la raíz del monorepo.
Depende de jay con `condition: service_healthy`, así que sin jay sano falco no
arranca.

## Configuración: tres capas, y `config.yaml` gana en local

Orden real (`internal/config/loader.go`): defaults en código → `config.yaml` de
la raíz del repo → variables de entorno.

**`config.yaml` está versionado y es el que manda en un `go run` a secas.** Fija
`server.port: 8080`, `storage.default: local` (filesystem en `./data/images`) y
`security.api_key_required: false`. O sea: un falco arrancado a mano no escucha
en 4009, no habla con jay y no pide auth. El 4009, el backend jay y las claves
salen del bloque `falco` del `docker-compose.yml` de la raíz, que es la
configuración de verdad del stack.

El inventario de variables está en `getEnvMappings()` de
`internal/config/loader.go`, más el auto-descubrimiento por patrón
`STORAGE_BUCKET_<NAME>_<SUFIJO>` (`_TYPE`, `_ADDR`, `_ADMIN_ADDR`, `_TOKEN_ID`,
`_TOKEN_SECRET`, `_BUCKET`, `_POOL_SIZE`, …). Léelo de ahí; no lo copies aquí.

Las que **no** pasan por ese mapa y se leen con `os.Getenv`:

| Variable | Dónde se lee | Si falta |
|---|---|---|
| `HMAC_REQUIRE_EXPIRY` | `internal/api/handlers/delivery.go` | `/api/v1/images/*` responde 500. Es a propósito: el default sería aceptar URLs firmadas que nunca caducan. |
| `PROXY_ALLOWED_HOSTS` | `internal/api/handlers/proxy.go` | cae a un allowlist compilado |
| `WATERMARK_ALLOWED_HOSTS` | `internal/api/handlers/watermark_source.go` | `?wm_url=` responde 403. A propósito no hay fallback: abrirlo convertiría una URL de imagen en un fetch externo arbitrario |
| `PROXY_MAX_WIDTH`, `PROXY_DEFAULT_QUALITY` | `internal/api/handlers/proxy.go` | caen a sus constantes |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_DEPLOYMENT_ENV` | `internal/telemetry` | telemetría apagada, el arranque sigue |

`API_KEY_REQUIRED=true` **obliga** a `HMAC_REQUIRED=true`: `validateSecurity` se
niega a arrancar si no, porque un `<img>` no puede llevar una API key y la ruta
de delivery quedaría sin ninguna protección.

## Arquitectura

Las rutas se montan en `internal/api/server.go`; esa función es el inventario.
Lo que hay que saber antes de tocarla:

- `/api/v1/upload|update|list|delete|sign` van dentro del grupo autenticado
  (scoped keys si hay alguna configurada, si no la `API_KEY` única).
- `/api/v1/images/*` (delivery) y `/api/v1/proxy/*` quedan **fuera** de ese
  grupo a propósito y se autorizan solas: firma HMAC, o API key + scope cuando
  `HMAC_REQUIRED=false`. No las muevas al grupo autenticado.
- `/metrics` y `/debug/pprof/*` sólo se montan con `ENABLE_METRICS` /
  `ENABLE_PPROF`, y detrás de la API key.

**El upload no guarda el original.** `prepareForStorage` corre `Process` sobre
lo subido y almacena el resultado reencodado a `DEFAULT_FORMAT` (webp) salvo que
el request pida `?f=`. SVG, HTML y XML se rechazan con 415; el resto de content
types que no son imagen pasan tal cual, sin tocar. El id sale del hash del
contenido crudo, así que subir dos veces lo mismo cae en la misma clave y jay,
que es idempotente por clave, lo trata como no-op.

**Delivery tiene dos caminos.** Sin transformaciones ni formato, se hace stream
directo desde jay (`deliverRaw`) y no se cachea: no hay CPU que compartir y el
streaming mantiene la memoria plana. Con transformaciones o formato, la clave de
cache es computable sólo del query, así que se responde desde cache antes de
tocar jay; en miss, un `singleflight` comparte fetch + decode + encode entre
todos los requests concurrentes de la misma clave.

**El contrato de query params es `parseDeliveryParams`.** Dos clases, y la
diferencia es deliberada: lo que cambia geometría o encoding (`w`, `h`, `q`,
`f`, `fit`, `crop_*`, `rotate`, `flip`) rechaza con 400 si viene mal —servir
otra imagen sería peor que fallar—; lo cosmético (`maxage`, `smaxage`,
`gravity`, `pad_*`, `trim`, `orient`, `meta`, y todo el grupo de color:
`brightness`, `contrast`, `gamma`, `saturation`, `hue`, `blur`, `sharpen`) cae a
su default. Casi todos tienen alias largo (`w|width`, `q|quality`, `b|bucket`,
`d|dir|directory`) y la extensión del path (`/images/abc.webp`) actúa como
default de formato, que es lo que permite cachear por extensión en un CDN.

**La marca de agua es la excepción a la regla de "cosmético = default".** `wm`
(id en el propio storage) o `wm_url` (URL externa) se resuelven en
`fetchAndProcess`, o sea sólo en el miss de cache, y **cualquier fallo se
reporta** (403/404/422/502): una imagen servida sin la marca que se pidió es
idéntica a una que funcionó. `wm_url` exige `WATERMARK_ALLOWED_HOSTS`; sin esa
variable se rechaza, nunca se abre. `wm` se lee por el MISMO backend que la
imagen, así que un scope no se puede saltar pidiendo la marca de otro bucket.

**La cache es sólo de transformadas y vive en RAM.** Un reinicio la pierde
entera y el siguiente request paga jay + decode + encode; nunca pierde datos,
porque los originales están en jay, pero un redeploy en hora pico se nota.
`CACHE_SIZE_MB` es el techo (256 por default) y en `0` desactiva la cache por
completo. `CACHE_TTL_HOURS` es el TTL por entrada y `CACHE_CLEANUP_INTERVAL` la
frecuencia del barrido: son knobs distintos y confundirlos ya volvió a
`CACHE_TTL_HOURS` un no-op una vez.

## Quién consume falco

birdple (el SSR firma las URLs), birdple-api (`src/modules/images/`),
birdple_app y colibri. La firma HMAC está reimplementada en TypeScript en
`birdple/src/server/images/sign.ts` y
`birdple-api/src/modules/images/images.sign.ts`, con tests de paridad byte a
byte contra `internal/security/signature.go`. **Cambiar la canonicalización de
la firma rompe los tres a la vez**, y el síntoma es un 403 `INVALID_SIGNATURE`
en producción, no un test rojo aquí.

## JSON: `encoding/json/v2`

El código de producción usa `encoding/json/v2`; v1 sólo sobrevive en tests. Las
opciones están en `internal/jsonx/jsonx.go`:

| Perfil | Dónde | Por qué |
|---|---|---|
| `jsonx.Wire` | metadata que se persiste y la cache negativa | emite exactamente los bytes de v1; cambiarlos es un cambio de formato de datos |
| `jsonx.Lenient` | lectura de metadata vieja | acepta UTF-8 roto y llaves duplicadas para no volver ilegible un archivo escrito por una versión anterior |
| `jsonx.Strict` | bodies de nuestra API | rechaza campos desconocidos: un `{"qualty": 90}` da 400 en vez de ignorarse |

Dos cosas que se rompen fácil: en v2 `omitempty` **no** omite el cero, así que
los campos numéricos y booleanos llevan `omitzero` (slices y mapas se quedan en
`omitempty`, que ahí sí coincide); y quien custodia esto es
`internal/jsonx/jsonx_diff_test.go`, que compara v1 contra v2+`Wire` byte a
byte. Si falla, se ajusta el tag, nunca el test.

## Reglas del repo

- Los comentarios del código van en **inglés** (el proyecto es público) — por
  eso `misspell` está en el gate de lint. Es la excepción local a la regla del
  monorepo.
- Los mocks de `tests/mocks/` los genera mockery a partir de `.mockery.yml`; no
  los edites a mano.
- Después de un tag nuevo de jay:
  `go get -u github.com/ivangsm/jay@latest && go mod tidy` y `make check`.
- No comprimas `image/*`: la lista de tipos de `middleware.Compress` en
  `server.go` es una allowlist a propósito, porque webp/jpeg/png ya vienen
  comprimidos y gzipearlos quema CPU y suele agrandar el payload.

## Decisiones tomadas

- **`vips.Startup` recibe un `Config` explícito, no `nil`.** vipsgen lee un nil
  como "SIMD desactivado", lo que hace más lento cada encode. Ver `startVips()`.
- **`TRUSTED_PROXIES` vacío = sólo loopback.** `X-Forwarded-For` y `X-Real-IP`
  se ignoran salvo desde los CIDRs listados: fail-closed. Detrás de
  Nginx/Traefik/ELB hay que listar la subred del proxy o el rate limit por IP
  cuenta al proxy, no al cliente.
- **`robots.txt` prohíbe todo.** falco es origen de un CDN de imágenes, no
  contenido indexable.
- **`goroutineleak` en pprof.** Es el perfil que atrapa las goroutines
  fire-and-forget de `ReplicatedStorage`, que arrancan con `context.Background()`
  y a las que no espera ningún `WaitGroup`.

## Trampas conocidas

- **`falco/.env` local está viejo.** No está versionado y trae variables que ya
  no existen (`STORAGE_PRIMARY`) más `PORT=8080`. godotenv no
  pisa lo que ya está en el entorno, pero si dependes de él para arrancar vas a
  levantar un falco filesystem en 8080 creyendo que es el del stack.
- **Sin `API_KEY_REQUIRED` ni `HMAC_REQUIRED`, falco arranca abierto.** Ninguna
  de las dos tiene default en viper, así que ausente vale `false` y la
  validación no las exige. Esto contradice la regla del raíz sobre features
  opcionales sin configurar; en el stack lo tapa el docker-compose, que pone
  ambas en `"true"`.
- **`docs/` no es fuente de verdad; `site/` sí.** La documentación viva es el
  sitio Astro Starlight de `site/` (publicado en https://birdple.github.io/falco/
  por `.github/workflows/pages.yml`), escrito verificando contra el código. El
  README quedó en ~110 líneas y apunta ahí.
  `docs/ARCHITECTURE.md`, `TECHNICAL_SPEC.md`, `IMPLEMENTATION_ROADMAP.md` y
  `DEPLOYMENT_GUIDE.md` son documentos previos a la implementación actual (no
  mencionan jay, ni HMAC, ni el proxy externo, y siguen usando `STORAGE_PRIMARY`),
  y `REVIEW_GUIDE.md` es una auditoría con hallazgos ya arreglados. `openapi.yaml`
  sí se sirve en `/docs/openapi.yaml`, pero le faltan `/sign` y `/proxy`.
  Verifica contra el código antes de creerles.

## Release y CI

Todo vive en `.github/workflows/` y se apoya en scripts versionados, así que
cada paso se puede correr a mano:

| Workflow | Cuándo | Qué hace |
|---|---|---|
| `ci.yml` | push a `main`/`dev`, PR | test con `-race`, lint, y **compilar + arrancar** el binario en glibc y musl, más construir y arrancar la imagen |
| `release.yml` | tag `v*` | imagen multi-arch a `ghcr.io/birdple/falco`, cinco binarios y el release de GitHub |
| `pages.yml` | push a `main` con cambios en `site/` | publica el sitio de docs |

Los jobs de Go corren dentro de `ubuntu:26.04`: es la primera LTS con libvips
**8.18** en apt, que es la que exige `vipsgen/vips`. El `ubuntu-latest` del runner
trae 8.15 y no compila.

```bash
scripts/release-binary.sh 0.13.0 abc1234 dist      # nativo, con la libvips del host
scripts/build-in-container.sh musl 0.13.0 abc1234 dist
scripts/lint-in-container.sh                        # el lint de CI, en Linux
scripts/smoke-image.sh falco:ci 0.0.0-ci
scripts/release-notes.sh v0.13.0
```

**`make lint` en macOS no es el lint de CI.** Hay reglas cuyo resultado depende
de la plataforma: `unconvert` marcó `int64(stat.Bsize)` en Linux, donde
`Statfs_t.Bsize` ya es `int64`, mientras que en Darwin es `uint32` y la
conversión es obligatoria. Un `//nolint` tampoco sirve —con `allow-unused:
false`, en macOS se reporta como directiva sin usar—, así que la salida es
separar por plataforma con build tags. Antes de empujar, `make lint-linux`.

**Ningún artefacto se publica sin haberse arrancado.** `release-binary.sh` levanta
el binario en un directorio vacío (no en el repo: viper tomaría el `config.yaml`
versionado, que declara un bucket jay con credenciales) y exige que `/health`
reporte la versión inyectada. Un binario CGO que compila todavía no es un binario
que enlaza.

## Fuera de alcance

- **No hay DB ni NATS.** falco no persiste nada propio y no publica ni consume
  eventos: todo su estado son la cache en RAM y lo que vive en jay.
- **No hay migraciones ni seed.**
