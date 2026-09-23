package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ErrObjectStoreDisabled is returned by every operation when no object storage
// has been configured, so callers can degrade instead of failing.
var ErrObjectStoreDisabled = errors.New("object storage is not configured")

// StoredObject describes a payload held outside the database.
type StoredObject struct {
	Key         string
	ContentType string
	Size        int64
}

var (
	objectStoreOnce   sync.Once
	objectStoreClient *s3.Client
)

// objectStore returns the shared S3 client, or nil when object storage is off.
// The client is built once: it holds connection pools, and rebuilding it per
// request would leak sockets on the relay path.
func objectStore() *s3.Client {
	objectStoreOnce.Do(func() {
		config := system_setting.GetObjectStoreConfig()
		if config.Bucket == "" || config.Endpoint == "" {
			return
		}
		options := s3.Options{
			Region:       config.Region,
			BaseEndpoint: aws.String(config.Endpoint),
			UsePathStyle: config.ForcePathStyle,
			Credentials: credentials.NewStaticCredentialsProvider(
				config.AccessKey, config.SecretKey, ""),
		}
		// Share the outbound pool when the process has one. It is nil in tools
		// that use this package without booting the server, and handing the SDK
		// a nil client panics on the first request rather than falling back.
		if shared := GetHttpClient(); shared != nil {
			options.HTTPClient = shared
		}
		objectStoreClient = s3.New(options)
		common.SysLog("object storage enabled: bucket " + config.Bucket)
	})
	return objectStoreClient
}

// ObjectStoreEnabled reports whether payloads can be offloaded.
func ObjectStoreEnabled() bool {
	return objectStore() != nil
}

func objectStoreKey(key string) string {
	prefix := system_setting.GetObjectStoreConfig().Prefix
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}

// PutObject stores a payload. The reader is consumed in full.
//
// size must be the exact byte count: S3 requires a known Content-Length, and
// the SDK would otherwise buffer the whole body in memory to compute one.
func PutObject(ctx context.Context, key string, contentType string, size int64, body io.Reader) error {
	client := objectStore()
	if client == nil {
		return ErrObjectStoreDisabled
	}
	config := system_setting.GetObjectStoreConfig()
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(config.Bucket),
		Key:           aws.String(objectStoreKey(key)),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}

// GetObject opens a stored payload. The caller closes the reader.
func GetObject(ctx context.Context, key string) (io.ReadCloser, StoredObject, error) {
	client := objectStore()
	if client == nil {
		return nil, StoredObject{}, ErrObjectStoreDisabled
	}
	config := system_setting.GetObjectStoreConfig()
	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(config.Bucket),
		Key:    aws.String(objectStoreKey(key)),
	})
	if err != nil {
		return nil, StoredObject{}, fmt.Errorf("get object %s: %w", key, err)
	}
	stored := StoredObject{Key: key}
	if output.ContentType != nil {
		stored.ContentType = *output.ContentType
	}
	if output.ContentLength != nil {
		stored.Size = *output.ContentLength
	}
	return output.Body, stored, nil
}

// DeleteObjects removes stored payloads, reporting the first failure but
// continuing so one unreachable key cannot strand the rest of a purge.
func DeleteObjects(ctx context.Context, keys []string) error {
	client := objectStore()
	if client == nil {
		return ErrObjectStoreDisabled
	}
	config := system_setting.GetObjectStoreConfig()
	var firstErr error
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(config.Bucket),
			Key:    aws.String(objectStoreKey(key)),
		})
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("delete object %s: %w", key, err)
		}
	}
	return firstErr
}
