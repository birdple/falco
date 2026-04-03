# Guía de Revisión Exhaustiva — Falco

Documento de referencia para realizar auditorías técnicas completas del proyecto.
Cubre bugs conocidos/potenciales, seguridad, performance, calidad de código e infraestructura.

---

## Cómo usar este documento

1. Abre cada sección en orden o salta a la que corresponda a tu objetivo.
2. Cada ítem indica el **archivo concreto** y la **línea/función** a revisar.
3. Los ítems marcados con 🔴 son críticos (pueden causar incidentes en producción).
4. Los marcados con 🟡 son mejoras importantes. Los 🟢 son nice-to-have.

---

## 1. Bugs conocidos y regresiones potenciales

### 1.1 Debug print en producción 🔴
**Archivo:** `internal/api/handlers/base.go` → `NewHandler()`

```go
fmt.Println("!!! NewHandler CALLED !!!")  // ELIMINAR
```

Contamina logs estructurados de logrus. En producción con múltiples workers genera ruido constante en stdout.

**Fix:** Eliminar esa línea.

---

### 1.2 AVIF silently downgrades a WebP 🟡
**Archivo:** `internal/processor/vips_processor.go` → `encodeImage()`

```go
case FormatAVIF:
    err = img.HeifsaveTarget(...)
    if err != nil {
        err = img.WebpsaveTarget(...)  // cliente pidió AVIF, recibe WebP sin aviso
    }
```

El cliente envía `format=avif`, recibe WebP y el `Content-Type` es `image/webp`. No hay log de warning ni header que indique el fallback. El cliente no puede distinguir si libvips soporta AV1 o no.

**Preguntas a responder:**
- ¿La imagen de producción tiene libvips compilado con soporte AV1?
- ¿Se debe retornar un error 415 en lugar de silenciar?

---

### 1.3 Race condition potencial en ResizeMode con gravity 🟡
**Archivo:** `internal/processor/vips_processor.go` → `applyTransformations()`

Cuando `params.Gravity != ""` se llama `ThumbnailImage` y se salta el bloque `resizeImage`. Pero si `params.Width == 0 && params.Height == 0` con `Gravity` seteado, ambos ramos del `if/else` se evalúan incorrectamente — se entra al bloque de gravity con `w = img.Width()` y `h = img.Height()`, efectivamente haciendo un thumbnail a las dimensiones originales, desperdiciando CPU.

**Fix:** Agregar guard `if params.Gravity != "" && (params.Width > 0 || params.Height > 0)`.

---

### 1.4 Metadata.Format vacío puede causar re-encoding innecesario 🟡
**Archivo:** `internal/api/handlers/delivery.go`

```go
needsProcessing := hasTransformations ||
    (params.Format != "" && params.Format != metadata.Format) ||
    (metadata.Format == "" || metadata.ContentType == "application/octet-stream")
```

Si MinIO no devuelve `Content-Type` (objeto subido por terceros), `metadata.ContentType` es vacío, no `application/octet-stream`. La condición nunca se cumple para ese caso y se sirve el objeto sin detectar el formato real.

**Verificar:** `internal/storage/minio.go` → `Retrieve()`: qué devuelve `info.ContentType` cuando no está definido en los object tags.

---

### 1.5 `ExtractArea` ignorado si falla en Trim 🟢
**Archivo:** `internal/processor/vips_processor.go`

```go
if err == nil && width > 0 && height > 0 {
    _ = img.ExtractArea(left, top, width, height)  // error ignorado
}
```

Si `ExtractArea` falla (coordenadas fuera de rango por un bug en FindTrim), se continúa con la imagen original sin ninguna señal de error.

---

### 1.6 `RedisCache` tiene contexto fijo en background 🟡
**Archivo:** `internal/cache/redis.go`

```go
ctx    context.Context  // se asigna context.Background() en el constructor
```

Las operaciones de Redis no usan el contexto de la request. Si el cliente cancela, las ops de Redis no se cancelan. En spikes de tráfico esto puede acumular goroutines bloqueadas en Redis.

**Fix:** Pasar `ctx` como parámetro a `Get`/`Set` en lugar de usar el contexto guardado.

---

## 2. Seguridad

### 2.1 Path traversal en FilesystemStorage 🔴
**Archivo:** `internal/storage/filesystem.go` → `getFilePath()`

Verificar que la ruta resultante siempre esté contenida dentro de `basePath`. Buscar si se valida con `filepath.Clean` y `strings.HasPrefix`:

```bash
grep -n "filepath.Clean\|HasPrefix\|basePath" internal/storage/filesystem.go
```

Si no existe esta validación, una key como `../../etc/passwd` podría leer o escribir fuera del directorio de datos.

---

### 2.2 HMAC: signing y verificación usan distinto orden de operaciones 🔴
**Archivo:** `internal/security/signature.go`

```go
mac.Write(salt)
mac.Write([]byte(str))
```

Verificar que **tanto** `SignURL` como `VerifyURL` llamen a `computeSignature` con los mismos argumentos y en el mismo orden. Actualmente parece correcto, pero confirmar con el test de round-trip:

```bash
grep -n "SignURL\|VerifyURL" internal/security/signature_test.go
```

---

### 2.3 API key transmitida en query param 🟡
**Archivo:** `internal/api/middleware/security.go` → `Handler()`

```go
// Verificar si acepta API key via ?api_key=...
```

Si el middleware acepta la key como query param (además de header), esta queda en access logs de nginx/proxies en texto claro. Confirmar que solo se acepte via `Authorization` o `X-API-Key` header.

---

### 2.4 CSP demasiado permisiva para la UI 🟡
**Archivo:** `internal/api/middleware/security.go` → `SecurityHeaders()`

```go
"script-src 'self' https://cdn.redoc.ly blob: 'unsafe-eval'; "
```

`unsafe-eval` permite `eval()` en JavaScript. Fue agregado para ReDoc pero aplica globalmente incluyendo la UI en `/`. Separar la CSP por ruta o reemplazar ReDoc por una versión que no requiera `unsafe-eval`.

---

### 2.5 Rate limiter no diferencia entre endpoints 🟡
**Archivo:** `internal/api/middleware/security.go`

El rate limiter aplica el mismo límite a todas las rutas. El endpoint de upload debería tener un límite mucho más bajo que el de delivery. Revisar si hay configuración diferenciada por ruta o si hay que agregarla.

---

### 2.6 Logs exponen paths de imágenes en warnings de firma 🟢
**Archivo:** `internal/api/handlers/delivery.go`

```go
h.logger.WithField("path", r.URL.Path).Warn("Invalid signature")
```

El path puede contener información sensible sobre la estructura de almacenamiento. Evaluar si se debe loggear o solo incrementar un contador de Prometheus.

---

### 2.7 Directorio `web/static` sirve archivos sin restricción de extensión 🟡
**Archivo:** `internal/api/server.go`

```go
r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticDir)))
```

`http.FileServer` sirve cualquier archivo en el directorio, incluyendo `.map`, backups, etc. Considerar un handler que solo sirva extensiones conocidas (`.css`, `.js`, `.ico`, `.png`).

---

## 3. Performance

### 3.1 `io.ReadAll` en cada request de delivery carga imagen completa en memoria 🔴
**Archivo:** `internal/processor/vips_processor.go` → `Process()`

```go
inputData, err := io.ReadAll(input)
```

Cada imagen se lee completa en un buffer antes de procesarse. Para imágenes de 10MB con 100 req/s concurrentes = 1GB RAM solo en buffers de entrada, antes de contar el procesamiento de vips.

**Considerar:**
- Streaming directo a vips si la API lo permite
- Límite de tamaño antes de `ReadAll` (verificar si ya existe en el handler)
- Pool de buffers grandes similar al `bufferPool` ya existente

---

### 3.2 Cache key incluye el contenido completo de la imagen (SHA-256 de inputData) 🟡
**Archivo:** `internal/processor/vips_processor.go` → `generateCacheKey()`

```go
hash := sha256.Sum256(inputData)  // hashing de toda la imagen en cada request
```

Para un archivo de 5MB, esto es ~5ms de CPU solo para generar la key. Con cache hit ratio alto esto podría aliviarse usando la storage key + etag del objeto como cache key en lugar del contenido.

---

### 3.3 `bufferPool` no limita el tamaño máximo de buffers retenidos 🟡
**Archivo:** `internal/processor/vips_processor.go`

```go
var bufferPool = sync.Pool{
    New: func() interface{} { return bytes.NewBuffer(make([]byte, 0, 2*1024*1024)) },
}
```

Si se procesa una imagen de 50MB, el buffer crece a 50MB y vuelve al pool. La siguiente goroutine que tome ese buffer tendrá un backing array de 50MB aunque solo necesite 2MB. Considerar descartar buffers que excedan un umbral antes de devolverlos al pool.

---

### 3.4 Lock global en FilesystemStorage bloquea lecturas concurrentes 🟡
**Archivo:** `internal/storage/filesystem.go`

```go
fs.mu.Lock()    // en Store
fs.mu.RLock()   // verificar si Retrieve usa RLock
```

Confirmar que `Retrieve` usa `RLock` y no `Lock`. Si usa `Lock`, todas las lecturas concurrentes se serializan.

```bash
grep -n "mu\." internal/storage/filesystem.go
```

---

### 3.5 Vips `Autorot` llamado aunque la imagen no tenga EXIF 🟢
**Archivo:** `internal/processor/vips_processor.go`

```go
_ = img.Autorot(nil)  // non-fatal pero tiene overhead
```

`AutoOrient` se activa por defecto (el campo se setea a `true` en el handler). Considerar si vale la pena hacerlo solo para formatos que pueden tener EXIF (JPEG, HEIC) y saltarlo para PNG/WebP/GIF.

---

### 3.6 Métricas de tamaño de imagen eliminadas pero no reemplazadas 🟢
**Archivo:** `internal/api/handlers/delivery.go`

```go
// Removed: m.ImageProcessingSize.WithLabelValues("input").Observe(...)
// Removed: m.CacheHits.Inc() / m.CacheMisses.Inc()
```

Se eliminaron métricas en el refactor pero no se comprobó si siguen siendo usadas en el dashboard de Grafana (`falco-dashboard.json`). Verificar que el dashboard no tenga paneles rotos.

```bash
grep -n "ImageProcessingSize\|CacheHits\|CacheMisses" \
  deployments/monitoring/grafana/provisioning/dashboards/falco-dashboard.json
```

---

## 4. Calidad de código y mantenibilidad

### 4.1 Handler de entrega demasiado largo 🟡
**Archivo:** `internal/api/handlers/delivery.go`

La función `HandleDelivery` tiene ~280 líneas. Candidatos a extraer:
- Verificación HMAC → `verifySignature(r) error`
- Parseo de params de transformación → `parseTransformParams(r) (*ProcessingParams, error)`
- Lógica de `needsProcessing` → función o método separado

---

### 4.2 Constantes duplicadas o sin uso 🟢
**Archivo:** `internal/api/handlers/constants.go`

```bash
# Verificar si todas las constantes se usan después del refactor de delivery
grep -rn "MinDimensionPixels\|MaxBlurValue\|MaxSharpenValue\|FlipHorizontal\|FlipVertical\|MinBrightnessValue" \
  internal/api/handlers/
```

Tras el refactor de `delivery.go` que eliminó los parámetros de brillo/contraste/saturación/flip del handler, muchas de estas constantes pueden haber quedado sin uso.

---

### 4.3 `internal/processor/` tiene archivos redundantes 🟡
El directorio tiene: `decoder.go`, `encoder.go`, `transformations.go`, `pipeline.go`, `processor.go` **y** `vips_processor.go`.

Verificar si `transformations.go` y `encoder.go`/`decoder.go` son usados o si fueron reemplazados por la implementación en `vips_processor.go`:

```bash
grep -rn "func " internal/processor/transformations.go internal/processor/encoder.go \
  internal/processor/decoder.go | awk -F: '{print $3}' | grep "^func" | \
  sed 's/func //' | sed 's/(.*$//' | while read fn; do
    count=$(grep -rn "$fn" internal/processor/ | grep -v "_test\|decoder.go\|encoder.go\|transformations.go" | wc -l)
    echo "$fn: $count usos externos"
  done
```

---

### 4.4 Tests no cubren el módulo `security` en integración 🟡
**Archivo:** `internal/security/signature_test.go`

Verificar que exista un test de round-trip completo: `SignURL → URL → VerifyURL` con distintos valores de `signatureSize` incluyendo el edge case `signatureSize = 0` (sin truncamiento).

```bash
grep -n "func Test" internal/security/signature_test.go
```

---

### 4.5 Tests de handlers no cubren el nuevo endpoint `/sign` 🟡
**Archivo:** `internal/api/handlers/sign.go`

```bash
grep -rn "sign\|HandleSignURL" tests/
```

Si no hay tests para `HandleSignURL`, agregar casos:
- Request sin body → 400
- Body con path vacío → 400  
- HMAC no configurado → respuesta apropiada
- Round-trip sign + verify

---

### 4.6 `list_query.go` en la raíz del repositorio 🟢
**Archivo:** `list_query.go` (raíz del proyecto)

Archivo suelto sin package claro. Verificar si es código temporal, un script de prueba, o pertenece a algún paquete. Mover o eliminar.

---

## 5. Infraestructura y deployment

### 5.1 `docker-compose.yml` expone PostgreSQL en puerto 5433 sin autenticación fuerte 🟡
**Archivo:** `docker-compose.yml`

```yaml
POSTGRES_PASSWORD: falco_password
ports:
  - "5433:5432"
```

En desarrollo está bien, pero el compose de prod (`docker-compose.prod.yml`) debería usar secrets de Docker o variables de entorno externas, no valores hardcodeados.

---

### 5.2 Dockerfile no tiene healthcheck definido 🟡
**Archivo:** `Dockerfile`

El HEALTHCHECK está comentado:

```dockerfile
# HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
#     CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1
```

Sin healthcheck, Docker Swarm y Kubernetes no pueden detectar un contenedor "running but unhealthy". Descomentar y ajustar el `start-period` si vips tarda en iniciar.

---

### 5.3 `tailwindcss-macos-arm64` binario en el repo 🟡
**Archivo:** `tailwindcss-macos-arm64` (raíz)

Binario de 12MB comprometido directamente en el repo. Debería estar en `.gitignore` y descargarse en el build step o agregarse a un `Makefile` target. Verificar:

```bash
git ls-files tailwindcss-macos-arm64
```

---

### 5.4 `web/static/css/output.css` debería generarse en CI, no commitearse 🟢
**Archivo:** `web/static/css/output.css`

El CSS compilado de Tailwind generalmente se genera en CI/build y se excluye del repo. Commitear el output dificulta los diffs y puede quedar desincronizado con `input.css`.

---

### 5.5 `configs/` no tiene config de ejemplo para producción 🟢
**Archivos:** `configs/`

```bash
ls configs/
```

Verificar si existe un `config.production.yaml` de referencia o si toda la configuración depende exclusivamente de variables de entorno (`.env.example`).

---

## 6. Observabilidad

### 6.1 Verificar métricas en el dashboard de Grafana tras el refactor 🔴
Como se mencionó en §3.6, el refactor de `delivery.go` eliminó métricas que pueden estar referenciadas en el dashboard. Abrir Grafana y revisar cada panel por errores de "No data" o queries rotas.

**Métricas eliminadas/movidas a verificar:**
- `falco_cache_hits_total`
- `falco_cache_misses_total`
- `falco_image_processing_size_bytes`

---

### 6.2 No hay tracing distribuido 🟢
El proyecto usa Prometheus + Grafana pero no tiene tracing (OpenTelemetry, Jaeger). Para requests lentas es difícil identificar si el cuello de botella está en storage, procesamiento o red. Considerar añadir spans básicos en las operaciones de vips y storage.

---

### 6.3 Logs de error no incluyen request ID 🟡
**Archivo:** `internal/api/middleware/`

Verificar si existe un middleware que inyecte un `X-Request-ID` en el contexto y si los logs de handlers lo incluyen. Sin request ID es difícil correlacionar logs de una misma request en producción.

```bash
grep -rn "request.id\|RequestID\|X-Request-ID" internal/
```

---

## 7. Checklist de revisión rápida

Ejecutar estos comandos antes de cada release:

```bash
# 1. Debug prints olvidados
grep -rn "fmt.Print\|log.Print\|println" internal/ cmd/ --include="*.go"

# 2. Errores ignorados con _
grep -rn "_ = " internal/ cmd/ --include="*.go" | grep -v "_test.go"

# 3. TODO/FIXME pendientes
grep -rn "TODO\|FIXME\|HACK\|XXX" internal/ cmd/ --include="*.go"

# 4. Secrets hardcodeados
grep -rn "password\|secret\|apikey\|api_key" internal/ configs/ --include="*.go" --include="*.yaml" -i | grep -v "_test.go\|example\|default"

# 5. Context sin timeout
grep -rn "context.Background()" internal/ --include="*.go" | grep -v "_test.go\|cache/redis"

# 6. Tests con cobertura
go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out -o coverage.html

# 7. Race detector
go test -race ./...

# 8. Linter
golangci-lint run ./...

# 9. Vulnerabilidades en dependencias
go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
```

---

## 8. Áreas con mayor ROI de mejora

En orden de impacto esperado:

| Prioridad | Área | Impacto |
|-----------|------|---------|
| 🔴 1 | Eliminar `fmt.Println` de `base.go` | Logs limpios |
| 🔴 2 | Path traversal en `filesystem.go` | Seguridad crítica |
| 🔴 3 | Grafana dashboard tras refactor | Observabilidad rota |
| 🟡 4 | Contexto fijo en `RedisCache` | Estabilidad bajo carga |
| 🟡 5 | Cache key basada en storage key+etag | -5ms CPU por req |
| 🟡 6 | Separar `HandleDelivery` en subfunciones | Mantenibilidad |
| 🟡 7 | Tests para `HandleSignURL` | Cobertura de seguridad |
| 🟡 8 | Habilitar HEALTHCHECK en Dockerfile | Prod readiness |
| 🟢 9 | Eliminar binario `tailwindcss-macos-arm64` | Repo limpio |
| 🟢 10 | Request ID en logs | Debuggabilidad |
