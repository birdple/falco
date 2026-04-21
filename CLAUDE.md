# falco — Procesamiento de Imágenes

## Qué hace

Servicio Go de procesamiento de imágenes sobre **libvips**: resize, crop, watermark, conversión de formato, cache LRU in-memory y HMAC URL signing. Backend de almacenamiento **delegado a Jay** (único consumidor del protocolo binario nativo de Jay en el stack).

## Stack

- Go 1.26+, Chi router, govips (libvips ≥ 8.x)
- zerolog (JSON estructurado)
- Storage backend: `github.com/ivangsm/jay/proto/client` (protocolo TCP nativo)
- Cache: LRU in-memory (no Redis por default)
- Observabilidad: Prometheus metrics + OTel collector para traces

## Cómo arrancar

```bash
# Desde la raíz del monorepo, con jay ya corriendo:
docker compose up -d jay falco

# Dev local (requiere libvips instalado en el host):
cd falco && go run ./cmd/server
```

## Variables de entorno

Todas las variables marcadas **obligatorias** deben estar en `.env` — sin defaults.

| Variable | Obligatoria | Propósito |
|---|---|---|
| `PORT` | no (default 4009) | Puerto HTTP |
| `STORAGE_DEFAULT` | sí | Nombre del bucket por defecto (usar `jay`) |
| `STORAGE_BUCKET_JAY_TYPE` | sí | Tipo de backend (`jay`) |
| `STORAGE_BUCKET_JAY_ADDR` | sí | Dirección del protocolo nativo (`jay:4012`) |
| `STORAGE_BUCKET_JAY_ADMIN_ADDR` | sí | Dirección HTTP para GetStats — mismo puerto que S3 API (`jay:4010`) |
| `STORAGE_BUCKET_JAY_BUCKET` | sí | Nombre del bucket en Jay (`falco-images`) |
| `STORAGE_BUCKET_JAY_TOKEN_ID` | sí | Token ID de Jay (del seed) |
| `STORAGE_BUCKET_JAY_TOKEN_SECRET` | sí | Secret plano del token Jay |
| `STORAGE_BUCKET_JAY_POOL_SIZE` | no (default 4) | Tamaño del pool de conexiones TCP |
| `API_KEY` | sí | API key para autenticar uploads |
| `HMAC_KEY`, `HMAC_SALT` | sí | Firma HMAC de URLs |
| `HMAC_REQUIRED` | sí (sin default) | `true` activa verificación HMAC en delivery. Cuando es `false`, delivery ya NO queda abierto: cae al path de API-key + scope en `HandleDelivery`. |
| `HMAC_REQUIRE_EXPIRY` | sí (sin default, fail-closed) | `true` obliga a que todas las URLs firmadas traigan `?exp=<unix>` y que no hayan expirado. `false` acepta URLs sin expiry (sólo compat temporal). Se lee vía `os.Getenv` — si falta, `/api/v1/images/*` devuelve 500. |
| `CACHE_SIZE_MB` | no (default 256) | Cache LRU en MB |
| `DEFAULT_FORMAT` | no (default webp) | Formato de salida por default |
| `DEFAULT_QUALITY` | no (default 85) | Calidad JPEG/WebP |
| `TRUSTED_PROXIES` | no (default vacío → solo loopback `127.0.0.0/8`, `::1/128`) | Lista separada por comas de CIDRs/IPs de proxies confiables. Solo desde estas direcciones se respetan `X-Forwarded-For` / `X-Real-IP`; fail-closed si no se configura. Detrás de Nginx/Traefik/ELB hay que listar la subred del proxy o el rate-limit per-IP no cuenta al cliente real. |

## Estructura (lo no obvio)

- `internal/storage/jay.go` — backend custom que habla protocolo nativo con Jay. **Único backend en uso en este stack.** Los otros (`filesystem.go`, `s3.go`, `minio.go`, `r2.go`) quedan upstream pero no se usan.
- `internal/storage/factory.go` — registry pattern. `StorageTypeJay` se registra en `init()`.
- `internal/config/loader.go` — env vars se auto-descubren bajo `STORAGE_BUCKET_<NAME>_*`. Para jay se añadieron sufijos `_ADDR`, `_ADMIN_ADDR`, `_TOKEN_ID`, `_TOKEN_SECRET`, `_POOL_SIZE`.
- `cmd/server/main.go:buildBucketBackend` — mapea `config.BucketConfig.JayXxx` a `storage.StorageConfig.JayXxx`.

## Cómo funciona

**Upload:**
```
cliente → POST /api/v1/upload → libvips decode (w/h/format) → JayStorage.Store → client.PutObject TCP :4012 → bbolt tx + fs atomic write → response { id, url, w, h, size }
```

**Delivery:**
```
cliente → GET /api/v1/images/{id}?w=400 → LRU cache?
  hit  → serve (nunca toca Jay)
  miss → JayStorage.Retrieve (client.GetObject) → libvips transform → cache → serve
```

## Comunicación

- **Recibe HTTP de:** nadie todavía (la adopción por `birdple-api` / `birdple` / `colibri` es trabajo de specs futuras)
- **Habla con:** `jay:4012` por protocolo binario nativo. Falco es el único consumidor nativo del stack (dogfooding intencional)
- **Métricas:** `GET /metrics` (Prometheus)
- **OTel traces:** exportados a `otel-collector:4318`

## Endpoints principales

| Método | Ruta | Propósito |
|---|---|---|
| POST | `/api/v1/upload` | Subir imagen (multipart/form-data) |
| GET | `/api/v1/images/{id}` | Obtener imagen (con `?w=`, `?h=`, `?format=`) |
| DELETE | `/api/v1/images/{id}` | Borrar imagen |
| GET | `/health` | Health check |
| GET | `/metrics` | Prometheus |

## Gotchas

- **libvips en el host**: dev local requiere libvips instalado (`brew install vips` en macOS, `apt install libvips-dev` en Linux). El Dockerfile ya lo incluye.
- **Cache LRU in-memory**: reinicio del contenedor pierde la cache, pero los originales están seguros en Jay.
- **HMAC URL signing**: deshabilitado en dev (`HMAC_REQUIRED=false`). Activarlo antes de exponer Falco públicamente.
- **Actualizar cliente nativo de Jay**: después de un tag nuevo en `github.com/ivangsm/jay`, correr `cd falco && go get -u github.com/ivangsm/jay@latest && go mod tidy` y levantar los tests.
- **Arranca después de Jay**: `depends_on.jay.condition: service_healthy` en docker-compose. Si Jay no arranca, Falco no arranca.

---

## Falco es upstream público — no borrar funcionalidad upstream

Falco es un proyecto **open-source público**. La configuración en birdple-v2 (jay + LRU + API key simple + HMAC) es **una de muchas posibles**. Los siguientes módulos existen intencionalmente aunque birdple-v2 no los use — otros usuarios del proyecto sí pueden necesitarlos:

### Storage backends soportados upstream

Registrados en [`internal/storage/factory.go`](./internal/storage/factory.go) y disponibles para cualquier usuario de Falco:

| Archivo | Backend | Estado en birdple-v2 | Estado upstream |
|---|---|---|---|
| `internal/storage/jay.go` | Jay (protocolo binario nativo) | ✅ En uso | Específico de birdple |
| `internal/storage/s3.go` | AWS S3 | No usado | ✅ Funcional |
| `internal/storage/minio.go` | MinIO | No usado | ✅ Funcional |
| `internal/storage/r2.go` | Cloudflare R2 | No usado | ✅ Funcional |
| `internal/storage/filesystem.go` | Filesystem local | No usado | ✅ Funcional |

**No remover** ninguno de estos, ni sus campos en `StorageConfig`, ni sus ramas en `config/validator.go`. Son parte de la API pública de Falco.

### ReplicatedStorage (primary + N backups)

[`internal/storage/replicated.go`](./internal/storage/replicated.go) implementa replicación sync/async/read-fallback a múltiples backends. **birdple-v2 no lo configura**, pero es una feature válida del proyecto — usuarios pueden replicar S3 → R2, o jay → filesystem para backups. No remover.

### Redis cache

[`internal/cache/redis.go`](./internal/cache/redis.go) + `ENABLE_REDIS` / `REDIS_URL` envs. **birdple-v2 usa LRU in-memory**, pero Redis es la opción correcta para:
- Múltiples instancias de Falco compartiendo cache
- Persistencia de cache a través de reinicios
- Cache más grande que la RAM de un solo contenedor

No remover — es una decisión operativa, no dead code.

### Scoped API keys, groups, subgroups

Toda la lógica de auto-discovery en [`internal/config/loader.go`](./internal/config/loader.go) (funciones `discoverBucketsFromEnv`, `discoverGroupsFromEnv`, `discoverSubgroupsFromEnv`, `discoverBucketKeysFromEnv`, etc.) más [`internal/api/middleware/scoped_auth.go`](./internal/api/middleware/scoped_auth.go) permiten:

- Keys con acceso limitado a buckets específicos
- Agrupar buckets lógicamente (groups / subgroups)
- Keys por-grupo y por-subgrupo

**birdple-v2 usa la auth simple** (una sola `API_KEY`), por lo que `ScopedAPIKeyAuth.HasScopedKeys()` devuelve false y se cae al fallback `APIKeyAuth`. Esto es intencional — otros usuarios multi-tenant sí lo necesitan. No remover.

### Regla para auditorías

Cualquier auditoría que recomiende borrar estos módulos como "dead code" está aplicando el criterio incorrecto. El criterio correcto es:

1. ¿Lo usa birdple-v2? → puede que no.
2. ¿Es parte de la superficie pública de Falco? → **sí** (todos los anteriores).
3. ¿Lo necesitan otros usuarios de Falco? → sí, por eso existe.

Solo se remueve código si (1) no lo usa birdple-v2 **y** (2) no es parte de la API pública upstream.
