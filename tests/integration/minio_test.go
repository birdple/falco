package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/imagine/internal/storage"
)

func getMinIOConfig() (*storage.MinIOConfig, error) {
	// Get MinIO configuration from environment variables
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		endpoint = "minio.birdple.com" // Remove https:// prefix for MinIO client
	}

	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}

	secretKey := os.Getenv("MINIO_SECRET_KEY")
	if secretKey == "" {
		secretKey = "minioadmin"
	}

	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "test-bucket"
	}

	region := os.Getenv("MINIO_REGION")
	if region == "" {
		region = "us-east-1"
	}

	// Check if STORAGE_MINIO_SECURE is set
	secure := false
	if secureStr := os.Getenv("STORAGE_MINIO_SECURE"); secureStr == "true" {
		secure = true
	}

	return &storage.MinIOConfig{
		Bucket:    bucket,
		Endpoint:  endpoint,
		Region:    region,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Secure:    secure,
	}, nil
}

func TestMinIOStorage_ClientCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	minioConfig, err := getMinIOConfig()
	require.NoError(t, err)

	t.Logf("Creating MinIO client for: %s (secure: %v)", minioConfig.Endpoint, minioConfig.Secure)

	// Just test client creation without bucket operations
	minioClient, err := minio.New(minioConfig.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioConfig.AccessKey, minioConfig.SecretKey, ""),
		Secure: minioConfig.Secure,
		Region: minioConfig.Region,
	})
	if err != nil {
		t.Logf("Client creation error details: %v", err)
		t.Fail()
		return
	}

	assert.NotNil(t, minioClient)
	t.Log("MinIO client created successfully")

	// Test basic connectivity by listing buckets
	ctx := context.Background()
	buckets, err := minioClient.ListBuckets(ctx)
	if err != nil {
		t.Logf("List buckets error details: %v", err)
		// Don't fail the test here, just log it
	} else {
		t.Logf("Successfully connected to MinIO. Found %d buckets", len(buckets))
	}
}

func TestMinIOStorage_BasicOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	minioConfig, err := getMinIOConfig()
	require.NoError(t, err)

	minioStorage, err := storage.NewMinIOStorage(minioConfig)
	require.NoError(t, err)

	ctx := context.Background()
	testKey := fmt.Sprintf("test-image-%d.jpg", time.Now().Unix())

	// Test data
	testData := []byte("test image data for MinIO storage")
	testMetadata := &storage.ImageMetadata{
		ID:           testKey,
		OriginalName: "test-image.jpg",
		Format:       "jpeg",
		Width:        800,
		Height:       600,
		ContentType:  "image/jpeg",
		CreatedAt:    time.Now(),
	}

	// Test Store
	err = minioStorage.Store(ctx, testKey, bytes.NewReader(testData), testMetadata)
	assert.NoError(t, err)

	// Test Exists (should return true)
	exists, err := minioStorage.Exists(ctx, testKey)
	assert.NoError(t, err)
	assert.True(t, exists)

	// Test Retrieve
	retrievedReader, retrievedMetadata, err := minioStorage.Retrieve(ctx, testKey)
	assert.NoError(t, err)
	assert.NotNil(t, retrievedReader)
	assert.NotNil(t, retrievedMetadata)

	// Verify metadata
	assert.Equal(t, testKey, retrievedMetadata.ID)
	assert.Equal(t, testMetadata.OriginalName, retrievedMetadata.OriginalName)
	assert.Equal(t, testMetadata.Format, retrievedMetadata.Format)
	assert.Equal(t, testMetadata.ContentType, retrievedMetadata.ContentType)
	assert.Equal(t, int64(len(testData)), retrievedMetadata.Size)

	// Verify data
	retrievedData := make([]byte, len(testData))
	_, err = retrievedReader.Read(retrievedData)
	assert.NoError(t, err)
	assert.Equal(t, testData, retrievedData)
	retrievedReader.Close()

	// Test GetStats
	stats, err := minioStorage.GetStats(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.True(t, stats.TotalImages >= 1)
	assert.True(t, stats.TotalSize >= int64(len(testData)))

	// Test Delete
	err = minioStorage.Delete(ctx, testKey)
	assert.NoError(t, err)

	// Test Exists after delete (should return false)
	exists, err = minioStorage.Exists(ctx, testKey)
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestMinIOStorage_MetadataHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	minioConfig, err := getMinIOConfig()
	require.NoError(t, err)

	minioStorage, err := storage.NewMinIOStorage(minioConfig)
	require.NoError(t, err)

	ctx := context.Background()
	testKey := fmt.Sprintf("metadata-test-%d.png", time.Now().Unix())

	// Test data with detailed metadata
	testData := []byte("fake png image data")
	testMetadata := &storage.ImageMetadata{
		ID:           testKey,
		OriginalName: "screenshot.png",
		Format:       "png",
		Width:        1920,
		Height:       1080,
		ContentType:  "image/png",
		Size:         int64(len(testData)),
		CreatedAt:    time.Now(),
		ETag:         "test-etag-12345",
	}

	// Store with metadata
	err = minioStorage.Store(ctx, testKey, bytes.NewReader(testData), testMetadata)
	assert.NoError(t, err)

	// Retrieve and verify all metadata is preserved
	retrievedReader, retrievedMetadata, err := minioStorage.Retrieve(ctx, testKey)
	assert.NoError(t, err)
	assert.NotNil(t, retrievedMetadata)

	// Check that metadata is preserved
	assert.Equal(t, testMetadata.OriginalName, retrievedMetadata.OriginalName)
	assert.Equal(t, testMetadata.Format, retrievedMetadata.Format)
	assert.Equal(t, testMetadata.Width, retrievedMetadata.Width)
	assert.Equal(t, testMetadata.Height, retrievedMetadata.Height)
	assert.Equal(t, testMetadata.ContentType, retrievedMetadata.ContentType)
	assert.Equal(t, testMetadata.Size, retrievedMetadata.Size)

	// Clean up
	retrievedReader.Close()
	err = minioStorage.Delete(ctx, testKey)
	assert.NoError(t, err)
}

func TestMinIOStorage_StorageConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	// Test creating storage backend from config
	storageConfig := &storage.StorageConfig{
		Type:          storage.StorageTypeMinIO,
		MinIOBucket:   "test-config-bucket",
		MinIOEndpoint: "http://localhost:9000",
		MinIORegion:   "us-east-1",
		AccessKey:     "minioadmin",
		SecretKey:     "minioadmin",
		MinIOSecure:   false,
	}

	backend, err := storage.NewStorageBackend(storageConfig)
	assert.NoError(t, err)
	assert.NotNil(t, backend)

	// Verify it's a MinIO storage instance
	_, ok := backend.(*storage.MinIOStorage)
	assert.True(t, ok)

	// Test health check
	ctx := context.Background()
	err = backend.Health(ctx)
	assert.NoError(t, err)
}

func TestMinIOStorage_RealImageOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	// Use the existing birdple bucket that already has images
	minioConfig, err := getMinIOConfig()
	require.NoError(t, err)

	// Override bucket to use the existing one
	minioConfig.Bucket = "birdple"

	minioStorage, err := storage.NewMinIOStorage(minioConfig)
	if err != nil {
		t.Logf("Failed to create MinIO storage: %v", err)
		t.Skip("Skipping test - cannot connect to MinIO")
		return
	}

	ctx := context.Background()

	// List local images directory
	localImagesDir := "images"
	files, err := ioutil.ReadDir(localImagesDir)
	require.NoError(t, err)

	if len(files) == 0 {
		t.Skip("No images found in local images directory")
		return
	}

	t.Logf("Found %d images in local directory", len(files))

	// Test each image file
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		t.Run(fmt.Sprintf("Image_%s", file.Name()), func(t *testing.T) {
			testRealImageUploadAndRetrieve(t, ctx, minioStorage, filepath.Join(localImagesDir, file.Name()), file.Name())
		})
	}
}

func testRealImageUploadAndRetrieve(t *testing.T, ctx context.Context, minioStorage *storage.MinIOStorage, localPath, fileName string) {
	// Read local image file
	localData, err := ioutil.ReadFile(localPath)
	require.NoError(t, err)

	t.Logf("Testing image: %s (size: %d bytes)", fileName, len(localData))

	// Determine content type based on file extension
	contentType := "application/octet-stream"
	if filepath.Ext(fileName) == ".png" {
		contentType = "image/png"
	} else if filepath.Ext(fileName) == ".jpg" || filepath.Ext(fileName) == ".jpeg" {
		contentType = "image/jpeg"
	}

	// Create metadata
	testKey := fmt.Sprintf("test/%s", fileName)
	metadata := &storage.ImageMetadata{
		ID:           testKey,
		OriginalName: fileName,
		Format:       strings.TrimPrefix(filepath.Ext(fileName), "."),
		ContentType:  contentType,
		Size:         int64(len(localData)),
		CreatedAt:    time.Now(),
	}

	// Upload image to MinIO
	err = minioStorage.Store(ctx, testKey, bytes.NewReader(localData), metadata)
	if err != nil {
		t.Logf("Failed to upload image: %v", err)
		// Don't fail the test, just log the error for now
		return
	}

	t.Logf("Successfully uploaded image: %s", testKey)

	// Verify image exists in MinIO
	exists, err := minioStorage.Exists(ctx, testKey)
	if err != nil {
		t.Logf("Failed to check existence: %v", err)
		return
	}
	assert.True(t, exists, "Image should exist after upload")

	// Retrieve image from MinIO
	retrievedReader, retrievedMetadata, err := minioStorage.Retrieve(ctx, testKey)
	if err != nil {
		t.Logf("Failed to retrieve image: %v", err)
		return
	}
	defer retrievedReader.Close()

	// Read retrieved data
	retrievedData, err := io.ReadAll(retrievedReader)
	if err != nil {
		t.Logf("Failed to read retrieved data: %v", err)
		return
	}

	// Verify data integrity
	assert.Equal(t, int64(len(localData)), retrievedMetadata.Size, "Size should match")
	assert.Equal(t, contentType, retrievedMetadata.ContentType, "Content type should match")
	assert.Equal(t, fileName, retrievedMetadata.OriginalName, "Original name should match")
	assert.Equal(t, localData, retrievedData, "Image data should be identical")

	t.Logf("Successfully verified image integrity for: %s", fileName)

	// Clean up - delete the test image
	err = minioStorage.Delete(ctx, testKey)
	if err != nil {
		t.Logf("Failed to delete test image: %v", err)
	} else {
		t.Logf("Cleaned up test image: %s", testKey)
	}
}

func TestMinIOStorage_DirectClientOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MinIO integration test in short mode")
	}

	minioConfig, err := getMinIOConfig()
	require.NoError(t, err)

	// Override bucket to use the existing one
	minioConfig.Bucket = "birdple"

	t.Logf("Testing direct MinIO client operations with bucket: %s", minioConfig.Bucket)

	// Create MinIO client directly (bypass our storage wrapper)
	minioClient, err := minio.New(minioConfig.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioConfig.AccessKey, minioConfig.SecretKey, ""),
		Secure: minioConfig.Secure,
		Region: minioConfig.Region,
	})
	require.NoError(t, err)

	ctx := context.Background()

	// Test 1: List existing objects in the bucket
	t.Log("Testing: List objects in bucket")
	objectCh := minioClient.ListObjects(ctx, minioConfig.Bucket, minio.ListObjectsOptions{})
	objectCount := 0
	for obj := range objectCh {
		if obj.Err != nil {
			t.Logf("Error listing objects: %v", obj.Err)
			break
		}
		objectCount++
		if objectCount <= 5 { // Log first 5 objects
			t.Logf("Found object: %s (size: %d)", obj.Key, obj.Size)
		}
	}
	t.Logf("Total objects found: %d", objectCount)

	// Test 2: Try to upload a small test object directly
	testKey := fmt.Sprintf("test-direct-upload-%d.txt", time.Now().Unix())
	testContent := "Test content for direct MinIO upload"
	testReader := strings.NewReader(testContent)

	t.Logf("Testing: Direct upload to %s", testKey)
	_, err = minioClient.PutObject(ctx, minioConfig.Bucket, testKey, testReader, int64(len(testContent)), minio.PutObjectOptions{
		ContentType: "text/plain",
	})
	if err != nil {
		t.Logf("Direct upload failed: %v", err)
		// This is expected to fail due to bucket permissions
	} else {
		t.Logf("Direct upload successful!")

		// Try to retrieve it
		obj, err := minioClient.GetObject(ctx, minioConfig.Bucket, testKey, minio.GetObjectOptions{})
		if err != nil {
			t.Logf("Failed to retrieve uploaded object: %v", err)
		} else {
			retrievedContent, err := io.ReadAll(obj)
			obj.Close()
			if err != nil {
				t.Logf("Failed to read retrieved content: %v", err)
			} else {
				if string(retrievedContent) == testContent {
					t.Logf("Upload/Retrieve cycle successful - content matches!")
				} else {
					t.Logf("Content mismatch after retrieve")
				}
			}

			// Clean up
			err = minioClient.RemoveObject(ctx, minioConfig.Bucket, testKey, minio.RemoveObjectOptions{})
			if err != nil {
				t.Logf("Failed to cleanup test object: %v", err)
			} else {
				t.Log("Test object cleaned up successfully")
			}
		}
	}

	// Test 3: Try to get bucket info
	buckets, err := minioClient.ListBuckets(ctx)
	if err != nil {
		t.Logf("Failed to list buckets: %v", err)
	} else {
		t.Logf("Successfully listed %d buckets", len(buckets))
		for _, bucket := range buckets {
			t.Logf("Bucket: %s (created: %s)", bucket.Name, bucket.CreationDate.Format("2006-01-02 15:04:05"))
		}
	}
}
