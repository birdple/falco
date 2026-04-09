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
| `CACHE_SIZE_MB` | no (default 256) | Cache LRU en MB |
| `DEFAULT_FORMAT` | no (default webp) | Formato de salida por default |
| `DEFAULT_QUALITY` | no (default 85) | Calidad JPEG/WebP |

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
- **Único backend**: el upstream de Falco soporta múltiples backends (s3/minio/r2/filesystem). **No los uses** — el único backend en birdple-v2 es `jay`. La lógica de backups multi-target tampoco está en uso aquí.
- **Cache LRU in-memory**: reinicio del contenedor pierde la cache, pero los originales están seguros en Jay.
- **HMAC URL signing**: deshabilitado en dev (`HMAC_REQUIRED=false`). Activarlo antes de exponer Falco públicamente.
- **Actualizar cliente nativo de Jay**: después de un tag nuevo en `github.com/ivangsm/jay`, correr `cd falco && go get -u github.com/ivangsm/jay@latest && go mod tidy` y levantar los tests.
- **Arranca después de Jay**: `depends_on.jay.condition: service_healthy` en docker-compose. Si Jay no arranca, Falco no arranca.
