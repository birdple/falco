# ----------------------------------------
# Build stage (Etapa de Compilación)
# ----------------------------------------
FROM golang:1.25-alpine AS builder

# Instala las dependencias necesarias para compilar con vips
# - gcc, g++, musl-dev: Compilador C/C++ para CGO
# - vips-dev: Librería libvips y sus headers (necesita 8.17+ para vipsgen 1.1+)
# - pkgconf: pkg-config para detectar librerías
# IMPORTANTE: Alpine 3.22 tiene vips 8.16.1 que no incluye constantes necesarias
# (VIPS_INTENT_AUTO, VIPS_KERNEL_MKS2013, VIPS_KERNEL_MKS2021)
# Por eso usamos Alpine Edge que tiene vips 8.17+
RUN apk add --no-cache --repository=http://dl-cdn.alpinelinux.org/alpine/edge/community \
    --repository=http://dl-cdn.alpinelinux.org/alpine/edge/main \
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

# Compila el binario con optimizaciones
# Build tags:
#   - netgo: Usa DNS resolver nativo de Go (portable en Alpine)
#   - osusergo: Usa implementación Go para user/group ops (portable)
# Flags:
#   - ldflags "-w -s": Omite debug info y symbol table (reduce ~40% tamaño)
# Nota: No se puede hacer build estático porque vipsgen requiere CGO y libvips dinámica
RUN CGO_ENABLED=1 go build \
    -tags 'netgo osusergo' \
    -ldflags="-w -s" \
    -o imagine-server \
    cmd/server/main.go

# ----------------------------------------
# Runtime stage (Etapa de Ejecución)
# ----------------------------------------
# IMPORTANTE: Usar Alpine Edge para que las librerías coincidan con el build stage
# Si se usa Alpine 3.22, habrá errores de símbolos faltantes (symbol not found)
FROM alpine:edge

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
    && rm -rf /var/cache/apk/*

# Crea un usuario sin privilegios
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Crea los directorios necesarios
RUN mkdir -p /app/data /app/logs && \
    chown -R appuser:appgroup /app

# Establece el directorio de trabajo
WORKDIR /app

# Copia el binario desde la etapa 'builder'
COPY --from=builder /app/imagine-server .

# Copia los archivos de configuración
COPY --from=builder /app/configs ./configs

# Cambia la propiedad del binario y configs
RUN chown -R appuser:appgroup /app

# Cambia al usuario sin privilegios
USER appuser

# Expone el puerto
EXPOSE 8080

# Chequeo de salud
# HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
#     CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Comando de ejecución
CMD ["./imagine-server"]