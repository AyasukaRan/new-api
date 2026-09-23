package system_setting

import (
	"errors"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Object storage holds the payloads that cannot live in a database column —
// today the binary bodies behind a request trace, such as an uploaded audio
// file or a synthesized speech response.
//
// Configuration is environment-only rather than a database option: these are
// standing credentials for another system, and the options table is readable by
// every administrator and dumped into every backup.
const (
	ObjectStoreEndpointEnv       = "OBJECT_STORE_S3_ENDPOINT"
	ObjectStoreBucketEnv         = "OBJECT_STORE_S3_BUCKET"
	ObjectStoreRegionEnv         = "OBJECT_STORE_S3_REGION"
	ObjectStoreAccessKeyEnv      = "OBJECT_STORE_S3_ACCESS_KEY"
	ObjectStoreSecretKeyEnv      = "OBJECT_STORE_S3_SECRET_KEY"
	ObjectStorePrefixEnv         = "OBJECT_STORE_S3_PREFIX"
	ObjectStoreForcePathStyleEnv = "OBJECT_STORE_S3_FORCE_PATH_STYLE"
)

type ObjectStoreConfig struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Prefix    string
	// ForcePathStyle addresses buckets as endpoint/bucket/key rather than
	// bucket.endpoint/key. Self-hosted S3 implementations are commonly reached
	// by IP or by a single hostname with no wildcard certificate, where virtual
	// host addressing cannot resolve.
	ForcePathStyle bool
}

var objectStoreConfig ObjectStoreConfig

func init() {
	objectStoreConfig = LoadObjectStoreConfig()
}

// LoadObjectStoreConfig reads startup-only configuration. An invalid or partial
// configuration disables the store rather than failing startup: object storage
// is a diagnostic convenience, and a typo in it must not take the gateway down.
func LoadObjectStoreConfig() ObjectStoreConfig {
	config := ObjectStoreConfig{
		Endpoint:       strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStoreEndpointEnv, "")),
		Bucket:         strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStoreBucketEnv, "")),
		Region:         strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStoreRegionEnv, "us-east-1")),
		AccessKey:      strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStoreAccessKeyEnv, "")),
		SecretKey:      strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStoreSecretKeyEnv, "")),
		Prefix:         strings.Trim(strings.TrimSpace(common.GetEnvOrDefaultString(ObjectStorePrefixEnv, "")), "/"),
		ForcePathStyle: common.GetEnvOrDefaultBool(ObjectStoreForcePathStyleEnv, true),
	}
	if config.Endpoint == "" && config.Bucket == "" && config.AccessKey == "" {
		return ObjectStoreConfig{}
	}
	if err := ValidateObjectStoreConfig(config); err != nil {
		common.SysError("invalid object store configuration, object storage disabled: " + err.Error())
		return ObjectStoreConfig{}
	}
	return config
}

func ValidateObjectStoreConfig(config ObjectStoreConfig) error {
	if config.Endpoint == "" {
		return errors.New(ObjectStoreEndpointEnv + " is required")
	}
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" {
		return errors.New(ObjectStoreEndpointEnv + " must be an absolute URL, for example http://minio:9000")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New(ObjectStoreEndpointEnv + " must use http or https")
	}
	if config.Bucket == "" {
		return errors.New(ObjectStoreBucketEnv + " is required")
	}
	if !taskArtifactStoreBucketPattern.MatchString(config.Bucket) {
		return errors.New(ObjectStoreBucketEnv + " is not a valid bucket name")
	}
	if config.Region != "" && !taskArtifactStoreRegionPattern.MatchString(config.Region) {
		return errors.New(ObjectStoreRegionEnv + " is not a valid region")
	}
	if config.AccessKey == "" || config.SecretKey == "" {
		return errors.New(ObjectStoreAccessKeyEnv + " and " + ObjectStoreSecretKeyEnv + " are both required")
	}
	return nil
}

// GetObjectStoreConfig reports the configuration resolved at startup.
func GetObjectStoreConfig() ObjectStoreConfig {
	return objectStoreConfig
}

// ObjectStoreConfigured reports whether a usable configuration was supplied.
func ObjectStoreConfigured() bool {
	return objectStoreConfig.Bucket != "" && objectStoreConfig.Endpoint != ""
}
