// Package objstore writes archive objects through the S3 API (SeaweedFS in self-hosted
// installs, any S3 service in managed cloud). Only the S3 API is used, never a vendor SDK
// for a specific server.
package objstore

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Store puts objects.
type Store interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
}

// Config selects an S3-compatible endpoint.
type Config struct {
	Endpoint, Bucket, Region, AccessKey, SecretKey string
}

// New returns an S3 store, or nil when no endpoint is configured.
func New(c Config) Store {
	if c.Endpoint == "" {
		return nil
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	cl := s3.New(s3.Options{
		Region:       c.Region,
		BaseEndpoint: aws.String(c.Endpoint),
		UsePathStyle: true, // SeaweedFS and most self-hosted S3 servers
		Credentials:  credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
	})
	return &s3Store{cl: cl, bucket: c.Bucket}
}

type s3Store struct {
	cl     *s3.Client
	bucket string
}

func (s *s3Store) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.cl.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), ContentEncoding: aws.String("gzip"),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}
