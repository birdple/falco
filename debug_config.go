package main

import (
	"fmt"
	"os"

	"github.com/ivangsm/imagine/internal/config"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	fmt.Println("=== DEBUG: Testing .env loading ===")
	err := godotenv.Load()
	if err != nil {
		fmt.Printf(".env loading error: %v\n", err)
	} else {
		fmt.Println(".env loaded successfully")
	}

	fmt.Println("\n=== DEBUG: Environment Variables after .env loading ===")
	fmt.Printf("STORAGE_PRIMARY: %s\n", os.Getenv("STORAGE_PRIMARY"))
	fmt.Printf("STORAGE_MINIO_BUCKET: %s\n", os.Getenv("STORAGE_MINIO_BUCKET"))
	fmt.Printf("STORAGE_MINIO_ENDPOINT: %s\n", os.Getenv("STORAGE_MINIO_ENDPOINT"))
	fmt.Printf("STORAGE_MINIO_ACCESS_KEY: %s\n", os.Getenv("STORAGE_MINIO_ACCESS_KEY"))
	fmt.Printf("STORAGE_MINIO_SECRET_KEY: %s\n", os.Getenv("STORAGE_MINIO_SECRET_KEY"))
	fmt.Printf("STORAGE_MINIO_SECURE: %s\n", os.Getenv("STORAGE_MINIO_SECURE"))

	fmt.Println("\n=== DEBUG: Loading Configuration ===")

	cfg, err := config.Load()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load configuration")
		return
	}

	fmt.Printf("Config Storage.Primary: %s\n", cfg.Storage.Primary)
	fmt.Printf("Config Storage.MinIO.Bucket: %s\n", cfg.Storage.MinIO.Bucket)
	fmt.Printf("Config Storage.MinIO.Endpoint: %s\n", cfg.Storage.MinIO.Endpoint)
	fmt.Printf("Config Storage.MinIO.AccessKey: %s\n", cfg.Storage.MinIO.AccessKey)
	fmt.Printf("Config Storage.MinIO.SecretKey: %s\n", cfg.Storage.MinIO.SecretKey)
	fmt.Printf("Config Storage.MinIO.Secure: %v\n", cfg.Storage.MinIO.Secure)
	fmt.Printf("Config Storage.Local.Path: %s\n", cfg.GetLocalStoragePath())

	fmt.Println("\n=== DEBUG: Configuration loaded successfully ===")
}
