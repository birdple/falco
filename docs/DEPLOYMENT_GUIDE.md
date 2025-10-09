# Deployment Guide - Image Processing Service

## Quick Start

### Prerequisites
- Go 1.21+ installed
- Docker and Docker Compose
- AWS CLI configured (for S3 storage)
- MinIO server (optional, for MinIO storage)
- Make utility

### Local Development Setup

1. **Clone and Setup**
```bash
git clone <repository-url>
cd imagine
make setup
```

2. **Environment Configuration**
```bash
cp .env.example .env
# Edit .env with your configuration
```

3. **Run Locally**
```bash
# Development mode with hot reload
make dev

# Or run directly
go run cmd/server/main.go
```

4. **Run with Docker**
```bash
# Build and run
make docker-run

# Or use docker-compose
docker-compose up --build
```

## Production Deployment

### Docker Deployment

#### Single Container
```bash
# Build production image
docker build -t imagine-service:latest .

# Run container
docker run -d \
  --name imagine-service \
  -p 8080:8080 \
  -v /host/data:/app/data \
  -e STORAGE_LOCAL_PATH=/app/data/images \
  -e CACHE_SIZE_MB=512 \
  imagine-service:latest
```

#### Docker Compose Production
```yaml
# docker-compose.prod.yml
version: '3.8'

services:
  imagine-service:
    build:
      context: .
      dockerfile: Dockerfile
      target: production
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
      - ./logs:/app/logs
    environment:
      - ENV=production
      - PORT=8080
      - STORAGE_PRIMARY=filesystem
      - STORAGE_LOCAL_PATH=/app/data/images
      - CACHE_SIZE_MB=512
      - LOG_LEVEL=info
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf
      - ./ssl:/etc/nginx/ssl
    depends_on:
      - imagine-service
    restart: unless-stopped
```

### Kubernetes Deployment

#### Namespace and ConfigMap
```yaml
# k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: imagine-service

---
# k8s/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: imagine-config
  namespace: imagine-service
data:
  PORT: "8080"
  ENV: "production"
  STORAGE_PRIMARY: "filesystem"
  STORAGE_LOCAL_PATH: "/app/data/images"
  CACHE_SIZE_MB: "512"
  LOG_LEVEL: "info"
  DEFAULT_QUALITY: "85"
  DEFAULT_FORMAT: "webp"
```

#### Deployment and Service
```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: imagine-service
  namespace: imagine-service
spec:
  replicas: 3
  selector:
    matchLabels:
      app: imagine-service
  template:
    metadata:
      labels:
        app: imagine-service
    spec:
      containers:
      - name: imagine-service
        image: imagine-service:latest
        ports:
        - containerPort: 8080
        envFrom:
        - configMapRef:
            name: imagine-config
        - secretRef:
            name: imagine-secrets
        resources:
          requests:
            memory: "256Mi"
            cpu: "250m"
          limits:
            memory: "1Gi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
        volumeMounts:
        - name: image-storage
          mountPath: /app/data
      volumes:
      - name: image-storage
        persistentVolumeClaim:
          claimName: imagine-pvc

---
# k8s/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: imagine-service
  namespace: imagine-service
spec:
  selector:
    app: imagine-service
  ports:
  - port: 80
    targetPort: 8080
  type: ClusterIP
```

#### Persistent Volume and Ingress
```yaml
# k8s/pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: imagine-pvc
  namespace: imagine-service
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
  storageClassName: fast-ssd

---
# k8s/ingress.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: imagine-ingress
  namespace: imagine-service
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "10m"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "300"
spec:
  tls:
  - hosts:
    - images.yourdomain.com
    secretName: imagine-tls
  rules:
  - host: images.yourdomain.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: imagine-service
            port:
              number: 80
```

### AWS ECS Deployment

#### Task Definition
```json
{
  "family": "imagine-service",
  "networkMode": "awsvpc",
  "requiresCompatibilities": ["FARGATE"],
  "cpu": "512",
  "memory": "1024",
  "executionRoleArn": "arn:aws:iam::account:role/ecsTaskExecutionRole",
  "taskRoleArn": "arn:aws:iam::account:role/ecsTaskRole",
  "containerDefinitions": [
    {
      "name": "imagine-service",
      "image": "your-account.dkr.ecr.region.amazonaws.com/imagine-service:latest",
      "portMappings": [
        {
          "containerPort": 8080,
          "protocol": "tcp"
        }
      ],
      "environment": [
        {"name": "ENV", "value": "production"},
        {"name": "PORT", "value": "8080"},
        {"name": "STORAGE_PRIMARY", "value": "s3"},
        {"name": "STORAGE_S3_BUCKET", "value": "your-image-bucket"},
        {"name": "STORAGE_S3_REGION", "value": "us-west-2"}
      ],
      "secrets": [
        {
          "name": "STORAGE_S3_ACCESS_KEY",
          "valueFrom": "arn:aws:secretsmanager:region:account:secret:imagine/s3-access-key"
        },
        {
          "name": "STORAGE_S3_SECRET_KEY",
          "valueFrom": "arn:aws:secretsmanager:region:account:secret:imagine/s3-secret-key"
        }
      ],
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "awslogs-group": "/ecs/imagine-service",
          "awslogs-region": "us-west-2",
          "awslogs-stream-prefix": "ecs"
        }
      },
      "healthCheck": {
        "command": ["CMD-SHELL", "curl -f http://localhost:8080/health || exit 1"],
        "interval": 30,
        "timeout": 5,
        "retries": 3,
        "startPeriod": 60
      }
    }
  ]
}
```

### MinIO Deployment

#### Docker Compose with MinIO
```yaml
# docker-compose.minio.yml
version: '3.8'

services:
  minio:
    image: minio/minio:latest
    ports:
      - "9000:9000"
      - "9001:9001"
    volumes:
      - ./data/minio:/data
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    command: server /data --console-address ":9001"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 30s
      timeout: 20s
      retries: 3

  imagine-service:
    build:
      context: .
      dockerfile: Dockerfile
      target: production
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
    environment:
      - ENV=production
      - PORT=8080
      - STORAGE_PRIMARY=minio
      - STORAGE_MINIO_BUCKET=images
      - STORAGE_MINIO_ENDPOINT=http://minio:9000
      - STORAGE_MINIO_ACCESS_KEY=minioadmin
      - STORAGE_MINIO_SECRET_KEY=minioadmin
      - STORAGE_MINIO_SECURE=false
      - CACHE_SIZE_MB=512
      - LOG_LEVEL=info
    depends_on:
      minio:
        condition: service_healthy
    restart: unless-stopped
```

#### MinIO Setup Commands
```bash
# Create bucket for images
docker-compose -f docker-compose.minio.yml exec minio mc alias set local http://localhost:9000 minioadmin minioadmin
docker-compose -f docker-compose.minio.yml exec minio mc mb local/images
docker-compose -f docker-compose.minio.yml exec minio mc policy set public local/images

# Set bucket versioning (optional)
docker-compose -f docker-compose.minio.yml exec minio mc version enable local/images
```

#### Kubernetes with MinIO Operator
```yaml
# k8s/minio-tenant.yaml
apiVersion: minio.min.io/v2
kind: Tenant
metadata:
  name: imagine-storage
  namespace: imagine-service
spec:
  pools:
  - servers: 4
    volumesPerServer: 4
    size: 100Gi
    storageClassName: fast-ssd
  credentials:
    accessKey: minioadmin
    secretKey: minioadmin
  buckets:
  - name: images
  requestAutoCert: false
  certConfig: {}
```

## Configuration Management

### Environment Variables Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8080 | Server port |
| `HOST` | 0.0.0.0 | Server host |
| `ENV` | development | Environment (development/production) |
| `STORAGE_PRIMARY` | filesystem | Primary storage backend |
| `STORAGE_SECONDARY` | none | Secondary storage backend |
| `STORAGE_LOCAL_PATH` | ./data/images | Local storage path |
| `STORAGE_S3_BUCKET` | - | S3 bucket name |
| `STORAGE_S3_REGION` | us-west-2 | S3 region |
| `STORAGE_S3_ACCESS_KEY` | - | S3 access key |
| `STORAGE_S3_SECRET_KEY` | - | S3 secret key |
| `STORAGE_MINIO_BUCKET` | your-minio-bucket | MinIO bucket name |
| `STORAGE_MINIO_ENDPOINT` | http://localhost:9000 | MinIO server endpoint |
| `STORAGE_MINIO_REGION` | us-east-1 | MinIO region |
| `STORAGE_MINIO_ACCESS_KEY` | minioadmin | MinIO access key |
| `STORAGE_MINIO_SECRET_KEY` | minioadmin | MinIO secret key |
| `STORAGE_MINIO_SECURE` | false | Use HTTPS for MinIO connection |
| `CACHE_SIZE_MB` | 256 | Cache size in MB |
| `CACHE_TTL_HOURS` | 24 | Cache TTL in hours |
| `MAX_FILE_SIZE_MB` | 10 | Maximum file size |
| `DEFAULT_QUALITY` | 85 | Default image quality |
| `DEFAULT_FORMAT` | webp | Default output format |
| `CONCURRENT_WORKERS` | 4 | Processing workers |
| `API_KEY_REQUIRED` | false | Require API key |
| `API_KEY` | - | API key for authentication |
| `CORS_ORIGINS` | * | CORS allowed origins |
| `RATE_LIMIT_RPM` | 1000 | Rate limit per minute |
| `LOG_LEVEL` | info | Logging level |
| `LOG_FORMAT` | json | Log format (json/text) |

### Configuration Files

#### Development (.env.development)
```bash
ENV=development
PORT=8080
HOST=localhost
STORAGE_PRIMARY=filesystem
STORAGE_LOCAL_PATH=./data/images
CACHE_SIZE_MB=128
LOG_LEVEL=debug
LOG_FORMAT=text
API_KEY_REQUIRED=false
CORS_ORIGINS=http://localhost:3000,http://localhost:8080
```

#### Production (.env.production)
```bash
ENV=production
PORT=8080
HOST=0.0.0.0
STORAGE_PRIMARY=s3
STORAGE_SECONDARY=filesystem
STORAGE_LOCAL_PATH=/app/data/images
STORAGE_S3_BUCKET=prod-image-bucket
STORAGE_S3_REGION=us-west-2
CACHE_SIZE_MB=512
LOG_LEVEL=info
LOG_FORMAT=json
API_KEY_REQUIRED=true
RATE_LIMIT_RPM=1000
```

#### MinIO Production (.env.minio)
```bash
ENV=production
PORT=8080
HOST=0.0.0.0
STORAGE_PRIMARY=minio
STORAGE_SECONDARY=filesystem
STORAGE_LOCAL_PATH=/app/data/images
STORAGE_MINIO_BUCKET=prod-images
STORAGE_MINIO_ENDPOINT=https://minio.yourdomain.com
STORAGE_MINIO_REGION=us-east-1
STORAGE_MINIO_SECURE=true
CACHE_SIZE_MB=512
LOG_LEVEL=info
LOG_FORMAT=json
API_KEY_REQUIRED=true
RATE_LIMIT_RPM=1000
```

## Monitoring and Observability

### Health Checks
```bash
# Basic health check
curl http://localhost:8080/health

# Detailed health with storage status
curl http://localhost:8080/health?detailed=true
```

### Metrics Endpoints
```bash
# Prometheus metrics (if enabled)
curl http://localhost:8080/metrics

# Application metrics
curl http://localhost:8080/api/v1/stats
```

### Log Aggregation

#### ELK Stack Configuration
```yaml
# docker-compose.monitoring.yml
version: '3.8'

services:
  elasticsearch:
    image: docker.elastic.co/elasticsearch/elasticsearch:8.5.0
    environment:
      - discovery.type=single-node
      - xpack.security.enabled=false
    ports:
      - "9200:9200"

  logstash:
    image: docker.elastic.co/logstash/logstash:8.5.0
    volumes:
      - ./logstash.conf:/usr/share/logstash/pipeline/logstash.conf
    ports:
      - "5044:5044"
    depends_on:
      - elasticsearch

  kibana:
    image: docker.elastic.co/kibana/kibana:8.5.0
    ports:
      - "5601:5601"
    depends_on:
      - elasticsearch
```

### Alerting Rules

#### Prometheus Alerts
```yaml
# alerts.yml
groups:
- name: imagine-service
  rules:
  - alert: HighErrorRate
    expr: rate(http_requests_total{status=~"5.."}[5m]) > 0.1
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: High error rate detected

  - alert: HighMemoryUsage
    expr: process_resident_memory_bytes / 1024 / 1024 > 800
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: High memory usage detected

  - alert: StorageUnavailable
    expr: storage_health_status == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: Storage backend unavailable
```

## Security Hardening

### Container Security
```dockerfile
# Security-focused Dockerfile additions
FROM alpine:3.18 as production

# Create non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Set secure permissions
RUN mkdir -p /app/data && \
    chown -R appuser:appgroup /app && \
    chmod -R 755 /app

USER appuser

# Security labels
LABEL security.scan="enabled"
LABEL security.non-root="true"
```

### Network Security
```yaml
# nginx.conf security headers
server {
    listen 80;
    server_name images.yourdomain.com;
    
    # Security headers
    add_header X-Frame-Options DENY;
    add_header X-Content-Type-Options nosniff;
    add_header X-XSS-Protection "1; mode=block";
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains";
    
    # Rate limiting
    limit_req_zone $binary_remote_addr zone=upload:10m rate=10r/m;
    limit_req_zone $binary_remote_addr zone=api:10m rate=100r/m;
    
    location /api/v1/upload {
        limit_req zone=upload burst=5 nodelay;
        proxy_pass http://imagine-service:8080;
    }
    
    location /api/v1/ {
        limit_req zone=api burst=20 nodelay;
        proxy_pass http://imagine-service:8080;
    }
}
```

## Backup and Recovery

### Data Backup Strategy
```bash
#!/bin/bash
# backup.sh

BACKUP_DIR="/backups/$(date +%Y%m%d)"
mkdir -p $BACKUP_DIR

# Backup local images
if [ -d "/app/data/images" ]; then
    tar -czf $BACKUP_DIR/images.tar.gz /app/data/images
fi

# Backup configuration
cp /app/config.yaml $BACKUP_DIR/

# Upload to S3
aws s3 sync $BACKUP_DIR s3://backup-bucket/imagine-service/$(date +%Y%m%d)/

# Cleanup old backups (keep 30 days)
find /backups -type d -mtime +30 -exec rm -rf {} \;
```

### Disaster Recovery
```bash
#!/bin/bash
# restore.sh

RESTORE_DATE=$1
BACKUP_DIR="/backups/$RESTORE_DATE"

# Download from S3
aws s3 sync s3://backup-bucket/imagine-service/$RESTORE_DATE/ $BACKUP_DIR/

# Restore images
tar -xzf $BACKUP_DIR/images.tar.gz -C /

# Restore configuration
cp $BACKUP_DIR/config.yaml /app/

# Restart service
systemctl restart imagine-service
```

## Performance Tuning

### Go Runtime Optimization
```bash
# Environment variables for production
export GOGC=100
export GOMAXPROCS=4
export GOMEMLIMIT=800MiB
```

### System Optimization
```bash
# /etc/sysctl.conf optimizations
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 5000
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_keepalive_time = 600
net.ipv4.tcp_keepalive_intvl = 60
net.ipv4.tcp_keepalive_probes = 10
```

## Troubleshooting

### Common Issues

#### High Memory Usage
```bash
# Check memory usage
docker stats imagine-service

# Analyze heap dump
go tool pprof http://localhost:8080/debug/pprof/heap
```

#### Storage Issues
```bash
# Check disk space
df -h /app/data

# Check S3 connectivity
aws s3 ls s3://your-bucket --region us-west-2
```

#### Performance Issues
```bash
# Check CPU usage
docker exec imagine-service top

# Analyze CPU profile
go tool pprof http://localhost:8080/debug/pprof/profile
```

### Debug Mode
```bash
# Enable debug logging
export LOG_LEVEL=debug

# Enable pprof endpoints
export ENABLE_PPROF=true

# Run with race detection (development only)
go run -race cmd/server/main.go