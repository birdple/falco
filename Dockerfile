# ----------------------------------------
# Build stage (Etapa de Compilación)
# ----------------------------------------
FROM golang:1.25-alpine AS builder

# Instala las dependencias de C (GCC/G++) necesarias para CGO en Alpine.
RUN apk add --no-cache gcc g++ musl-dev

# Establece el directorio de trabajo
WORKDIR /app

# Copia los archivos de módulo
COPY go.mod go.sum ./

# Descarga las dependencias
RUN go mod download

# Copia el código fuente completo (¡Necesario para que CGO encuentre todo!)
COPY . .

# Compila el binario con optimizaciones
# *AÑADIDOS* -tags '...' para asegurar el enlace correcto de SQLite/CGO en Alpine.
RUN CGO_ENABLED=1 go build \
    -tags 'netgo osusergo static_build sqlite_omit_load_extension' \
    -a \
    -installsuffix cgo \
    -ldflags="-w -s" \
    -o imagine-server \
    cmd/server/main.go

# ----------------------------------------
# Runtime stage (Etapa de Ejecución)
# ----------------------------------------
FROM alpine:3.18

# Instala las dependencias de ejecución (certs y zona horaria)
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
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

# Cambia la propiedad del binario
RUN chown appuser:appgroup imagine-server

# Cambia al usuario sin privilegios
USER appuser

# Expone el puerto
EXPOSE 8080

# Chequeo de salud
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Comando de ejecución
CMD ["./imagine-server"]