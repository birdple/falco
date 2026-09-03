# ----------------------------------------
# Build stage (Etapa de Compilación)
# ----------------------------------------
# Pin explícito a Alpine 3.24 (no el "alpine" flotante de la imagen de Go) para
# que el builder y el runtime queden siempre en la misma versión de Alpine.
FROM golang:1.27-alpine3.24 AS builder

# Instala las dependencias necesarias para compilar con vips
# - gcc, g++, musl-dev: Compilador C/C++ para CGO
# - vips-dev: Librería libvips y sus headers (necesita 8.17+ para vipsgen 1.1+)
# - pkgconf: pkg-config para detectar librerías
# Alpine 3.24 trae vips-dev 8.18.2 en el repo community (habilitado por defecto),
# ya no hace falta el repo edge que usábamos cuando 3.22 traía 8.16.1.
RUN apk add --no-cache \
        gcc \
        g++ \
        musl-dev \
        vips-dev \
        pkgconf

# Establece el directorio de trabajo
WORKDIR /app

# Copia los archivos de módulo
COPY go.mod go.sum ./

# Descarga las dependencias
RUN go mod download

# Copia el código fuente completo (¡Necesario para que CGO encuentre todo!)
COPY . .

# Copia el archivo OpenAPI (excluido por .dockerignore pero necesario para /docs)
COPY docs/openapi.yaml ./docs/openapi.yaml

# Versión y commit que reporta /health. Vacíos por omisión: sin ellos el binario
# conserva el valor compilado en internal/version, en vez de reportar "dev" y
# hacer que un build local mienta sobre qué es.
ARG VERSION=""
ARG COMMIT=""

# Compila el binario con optimizaciones
# Build tags:
#   - netgo: Usa DNS resolver nativo de Go (portable en Alpine)
#   - osusergo: Usa implementación Go para user/group ops (portable)
# Flags:
#   - ldflags "-w -s": Omite debug info y symbol table (reduce ~40% tamaño)
#   - -trimpath: quita las rutas absolutas del builder del binario
# Nota: No se puede hacer build estático porque vipsgen requiere CGO y libvips dinámica
RUN set -eux; \
    LDFLAGS="-w -s"; \
    if [ -n "$VERSION" ]; then \
      LDFLAGS="$LDFLAGS -X github.com/birdple/falco/internal/version.Version=$VERSION"; \
    fi; \
    if [ -n "$COMMIT" ]; then \
      LDFLAGS="$LDFLAGS -X github.com/birdple/falco/internal/version.Commit=$COMMIT"; \
    fi; \
    CGO_ENABLED=1 go build \
      -tags 'netgo osusergo' \
      -trimpath \
      -ldflags="$LDFLAGS" \
      -o falco-server \
      ./cmd/server

# ----------------------------------------
# Runtime stage (Etapa de Ejecución)
# ----------------------------------------
# Misma versión de Alpine que el builder (3.24) para que la vips runtime
# coincida en versión (ABI) con la vips-dev usada al compilar.
FROM alpine:3.24

# Instala las dependencias de ejecución
# - ca-certificates: Certificados SSL
# - tzdata: Zonas horarias
# - vips: Librería libvips (runtime, sin headers de desarrollo) - versión 8.17+
# - wget: Para healthcheck
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    vips \
    wget \
    && rm -rf /var/cache/apk/* && \
    addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup && \
    mkdir -p /app/data /app/logs && \
    chown -R appuser:appgroup /app

# Establece el directorio de trabajo
WORKDIR /app

# Copia el binario desde la etapa 'builder'
COPY --from=builder /app/falco-server .

# Copia la documentación de la API
COPY --from=builder /app/docs ./docs

# Copia los archivos estáticos de la UI
COPY --from=builder /app/web ./web

# Cambia la propiedad de todos los archivos copiados
RUN chown -R appuser:appgroup /app

# Cambia al usuario sin privilegios
USER appuser

# Expone el puerto con el que se despliega en birdple-v2 (el compose de la raíz
# inyecta PORT=4009). Ojo: el default interno del binario sigue siendo 8080, por
# eso el healthcheck de abajo cae a 8080 cuando PORT no viene seteado.
EXPOSE 4009

# Healthcheck — usa $PORT para respetar el puerto real del deploy (PaaS suelen inyectar PORT=3000)
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider "http://localhost:${PORT:-8080}/health" || exit 1

# Comando de ejecución
CMD ["./falco-server"]