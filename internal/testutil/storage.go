package testutil

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

const (
	// The same build of MinIO that docker-compose.yml runs.
	minioImage    = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z"
	minioUser     = "minioadmin"
	minioPassword = "minioadmin"
	minioRegion   = "us-east-1"
)

// objects is the package's MinIO container, started by the first test that
// asks for a store.
var objects struct {
	mu        sync.Mutex
	container testcontainers.Container
	endpoint  string
	buckets   int
}

// NewStore returns object storage on a bucket that belongs to this test
// alone, on a real S3-compatible server. The package needs testutil.Main.
func NewStore(t testing.TB) *storage.S3 {
	t.Helper()
	ctx := context.Background()

	objects.mu.Lock()
	defer objects.mu.Unlock()

	if objects.container == nil {
		container, err := testcontainers.Run(ctx, minioImage,
			testcontainers.WithEnv(map[string]string{"MINIO_ROOT_USER": minioUser, "MINIO_ROOT_PASSWORD": minioPassword}),
			testcontainers.WithCmd("server", "/data"),
			testcontainers.WithExposedPorts("9000/tcp"),
			testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/live").WithPort("9000/tcp")),
		)
		require.NoError(t, err, "could not start MinIO (is Docker running?)")
		endpoint, err := container.PortEndpoint(ctx, "9000/tcp", "http")
		require.NoError(t, err)
		objects.container, objects.endpoint = container, endpoint
	}

	objects.buckets++
	cfg := storage.Config{
		Endpoint:        objects.endpoint,
		Region:          minioRegion,
		Bucket:          fmt.Sprintf("test-%d", objects.buckets),
		AccessKeyID:     minioUser,
		SecretAccessKey: minioPassword,
		UsePathStyle:    true,
	}

	admin := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: true,
	})
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
	require.NoError(t, err)

	return storage.NewS3(cfg)
}

// stopStore removes the MinIO container, if a test started one.
func stopStore() {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	if objects.container == nil {
		return
	}
	if err := testcontainers.TerminateContainer(objects.container); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: stop MinIO: %v\n", err)
	}
	objects.container = nil
}
