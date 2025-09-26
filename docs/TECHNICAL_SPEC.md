# Technical Specification - Image Processing Service

## Implementation Details

### 1. Storage Interface Design

```go
type StorageBackend interface {
    Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error
    Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
    Health(ctx context.Context) error
}

type ImageMetadata struct {
    ID          string    `json:"id"`
    OriginalName string   `json:"original_name"`
    Format      string    `json:"format"`
    Size        int64     `json:"size"`
    Width       int       `json:"width"`
    Height      int       `json:"height"`
    CreatedAt   time.Time `json:"created_at"`
    ContentType string    `json:"content_type"`
}
```

### 2. Image Processing Pipeline

```go
type ImageProcessor interface {
    Process(ctx context.Context, input io.Reader, params *ProcessingParams) (*ProcessedImage, error)
    GetMetadata(ctx context.Context, input io.Reader) (*ImageMetadata, error)
    SupportedFormats() []string
}

type ProcessingParams struct {
    Width   int     `json:"width,omitempty"`
    Height  int     `json:"height,omitempty"`
    Quality int     `json:"quality,omitempty"`
    Format  string  `json:"format,omitempty"`
    Fit     string  `json:"fit,omitempty"` // "cover", "contain", "fill"
}

type ProcessedImage struct {
    Data     io.ReadCloser
    Metadata *ImageMetadata
    CacheKey string
}
```

### 3. Cache Implementation

```go
type Cache interface {
    Get(key string) ([]byte, bool)
    Set(key string, value []byte, ttl time.Duration) error
    Delete(key string) error
    Clear() error
    Stats() CacheStats
}

type CacheStats struct {
    Hits        int64 `json:"hits"`
    Misses      int64 `json:"misses"`
    Size        int64 `json:"size"`
    MaxSize     int64 `json:"max_size"`
    ItemCount   int   `json:"item_count"`
    HitRatio    float64 `json:"hit_ratio"`
}
```

## API Specification

### Upload Endpoint

**Endpoint**: `POST /api/v1/upload`

**Request Headers**:
```
Content-Type: multipart/form-data
Authorization: Bearer <token> (optional)
```

**Request Body (Multipart)**:
```
file: <binary image data>
quality: 85 (optional, 1-100)
format: webp (optional, jpeg|png|webp)
```

**Request Body (URL Upload)**:
```json
{
  "url": "https://example.com/image.jpg",
  "quality": 85,
  "format": "webp"
}
```

**Success Response (201)**:
```json
{
  "success": true,
  "data": {
    "id": "img_1234567890abcdef",
    "url": "/api/v1/images/img_1234567890abcdef",
    "original_name": "photo.jpg",
    "format": "webp",
    "size": 1024576,
    "dimensions": {
      "width": 1920,
      "height": 1080
    },
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

**Error Responses**:
```json
// 400 Bad Request
{
  "success": false,
  "error": {
    "code": "INVALID_FILE_TYPE",
    "message": "Unsupported file type. Supported formats: JPEG, PNG, WebP"
  }
}

// 413 Payload Too Large
{
  "success": false,
  "error": {
    "code": "FILE_TOO_LARGE",
    "message": "File size exceeds maximum limit of 10MB"
  }
}

// 422 Unprocessable Entity
{
  "success": false,
  "error": {
    "code": "PROCESSING_FAILED",
    "message": "Unable to process image: corrupted or invalid image data"
  }
}
```

### Delivery Endpoint

**Endpoint**: `GET /api/v1/images/{id}`

**Query Parameters**:
- `w` (int): Target width in pixels
- `h` (int): Target height in pixels  
- `q` (int): Quality level (1-100, default: 85)
- `f` (string): Output format (jpeg|png|webp, default: webp)
- `fit` (string): Resize mode (cover|contain|fill, default: cover)

**Example Requests**:
```
GET /api/v1/images/img_1234567890abcdef
GET /api/v1/images/img_1234567890abcdef?w=800&h=600&q=90&f=jpeg
GET /api/v1/images/img_1234567890abcdef?w=400&fit=contain
```

**Success Response (200)**:
```
Content-Type: image/webp (or appropriate format)
Content-Length: 1024576
Cache-Control: public, max-age=31536000
ETag: "img_1234567890abcdef_w800_h600_q90_webp"
Last-Modified: Mon, 01 Jan 2024 00:00:00 GMT

<binary image data>
```

**Error Responses**:
```json
// 404 Not Found
{
  "success": false,
  "error": {
    "code": "IMAGE_NOT_FOUND",
    "message": "Image with ID 'img_1234567890abcdef' not found"
  }
}

// 400 Bad Request
{
  "success": false,
  "error": {
    "code": "INVALID_PARAMETERS",
    "message": "Invalid transformation parameters: width must be between 1 and 4000"
  }
}
```

### Health Check Endpoint

**Endpoint**: `GET /health`

**Success Response (200)**:
```json
{
  "status": "healthy",
  "timestamp": "2024-01-01T00:00:00Z",
  "version": "1.0.0",
  "uptime": "72h30m15s",
  "storage": {
    "local": {
      "status": "healthy",
      "free_space": "50GB",
      "total_space": "100GB"
    },
    "s3": {
      "status": "healthy",
      "region": "us-west-2",
      "bucket": "my-images"
    }
  },
  "cache": {
    "status": "healthy",
    "hit_ratio": 0.85,
    "size": "256MB",
    "max_size": "512MB"
  }
}
```

## Configuration Schema

### Environment Variables

```bash
# Server Configuration
PORT=8080
HOST=0.0.0.0
ENV=production

# Storage Configuration
STORAGE_PRIMARY=filesystem
STORAGE_SECONDARY=s3
STORAGE_LOCAL_PATH=/app/data/images
STORAGE_S3_BUCKET=my-image-bucket
STORAGE_S3_REGION=us-west-2
STORAGE_S3_ACCESS_KEY=your-access-key
STORAGE_S3_SECRET_KEY=your-secret-key

# Cache Configuration
CACHE_SIZE_MB=512
CACHE_TTL_HOURS=24

# Processing Configuration
MAX_FILE_SIZE_MB=10
DEFAULT_QUALITY=85
DEFAULT_FORMAT=webp
CONCURRENT_WORKERS=4

# Security Configuration
API_KEY_REQUIRED=false
API_KEY=your-api-key
CORS_ORIGINS=*
RATE_LIMIT_RPM=1000

# Logging Configuration
LOG_LEVEL=info
LOG_FORMAT=json
```

### Configuration File (config.yaml)

```yaml
server:
  port: 8080
  host: "0.0.0.0"
  read_timeout: "30s"
  write_timeout: "30s"
  idle_timeout: "60s"
  shutdown_timeout: "30s"

storage:
  primary: "filesystem"
  secondary: "s3"
  local:
    path: "/app/data/images"
    create_dirs: true
  s3:
    bucket: "my-image-bucket"
    region: "us-west-2"
    endpoint: ""
    access_key: ""
    secret_key: ""

cache:
  size_mb: 512
  ttl_hours: 24
  cleanup_interval: "10m"

processing:
  max_file_size_mb: 10
  default_quality: 85
  default_format: "webp"
  concurrent_workers: 4
  supported_formats: ["jpeg", "png", "webp"]
  max_dimensions:
    width: 4000
    height: 4000

security:
  api_key_required: false
  api_key: ""
  cors:
    origins: ["*"]
    methods: ["GET", "POST", "OPTIONS"]
    headers: ["Content-Type", "Authorization"]
  rate_limit:
    requests_per_minute: 1000
    burst: 100

logging:
  level: "info"
  format: "json"
  output: "stdout"
```

## Error Handling Strategy

### Error Types

```go
type APIError struct {
    Code       string `json:"code"`
    Message    string `json:"message"`
    StatusCode int    `json:"-"`
    Details    map[string]interface{} `json:"details,omitempty"`
}

// Predefined error codes
const (
    ErrInvalidFileType     = "INVALID_FILE_TYPE"
    ErrFileTooLarge       = "FILE_TOO_LARGE"
    ErrProcessingFailed   = "PROCESSING_FAILED"
    ErrImageNotFound      = "IMAGE_NOT_FOUND"
    ErrInvalidParameters  = "INVALID_PARAMETERS"
    ErrStorageUnavailable = "STORAGE_UNAVAILABLE"
    ErrRateLimitExceeded  = "RATE_LIMIT_EXCEEDED"
    ErrUnauthorized       = "UNAUTHORIZED"
    ErrInternalError      = "INTERNAL_ERROR"
)
```

### HTTP Status Code Mapping

| Error Code | HTTP Status | Description |
|------------|-------------|-------------|
| INVALID_FILE_TYPE | 400 | Unsupported file format |
| FILE_TOO_LARGE | 413 | File exceeds size limit |
| PROCESSING_FAILED | 422 | Image processing error |
| IMAGE_NOT_FOUND | 404 | Requested image not found |
| INVALID_PARAMETERS | 400 | Invalid query parameters |
| STORAGE_UNAVAILABLE | 503 | Storage backend unavailable |
| RATE_LIMIT_EXCEEDED | 429 | Too many requests |
| UNAUTHORIZED | 401 | Invalid or missing API key |
| INTERNAL_ERROR | 500 | Unexpected server error |

## Performance Requirements

### Response Time Targets
- Upload endpoint: < 2 seconds for files up to 10MB
- Delivery endpoint (cached): < 100ms
- Delivery endpoint (uncached): < 1 second
- Health check: < 50ms

### Throughput Targets
- Concurrent uploads: 50 requests/second
- Image delivery: 1000 requests/second
- Cache hit ratio: > 80%

### Resource Limits
- Memory usage: < 1GB per instance
- CPU usage: < 80% under normal load
- Disk I/O: Optimized for SSD storage
- Network: Efficient streaming for large files

## Security Considerations

### Input Validation
- File type validation using magic numbers
- File size limits enforcement
- Image dimension limits
- Parameter sanitization

### Access Control
- Optional API key authentication
- Rate limiting per client IP
- CORS policy enforcement
- Request size limits

### Data Protection
- Secure file storage paths
- No directory traversal vulnerabilities
- Sanitized error messages
- Secure temporary file handling

## Monitoring and Metrics

### Key Metrics
- Request count and latency percentiles
- Error rates by endpoint and error type
- Cache hit/miss ratios
- Storage operation metrics
- Memory and CPU utilization
- Active goroutine count

### Logging Format
```json
{
  "timestamp": "2024-01-01T00:00:00Z",
  "level": "info",
  "message": "Image processed successfully",
  "request_id": "req_1234567890",
  "method": "POST",
  "path": "/api/v1/upload",
  "status_code": 201,
  "duration_ms": 1250,
  "image_id": "img_1234567890abcdef",
  "file_size": 1024576,
  "processing_time_ms": 800
}
```

## Testing Strategy

### Unit Tests
- Storage backend implementations
- Image processing functions
- Cache operations
- Configuration parsing
- Utility functions

### Integration Tests
- End-to-end API workflows
- Storage backend integration
- Image processing pipeline
- Error handling scenarios

### Performance Tests
- Load testing with concurrent requests
- Memory usage under load
- Cache performance
- Storage backend performance

### Security Tests
- Input validation testing
- File upload security
- Rate limiting verification
- Authentication testing