# ADR-005: S3 API only, SeaweedFS for development and self-hosting

- Status: accepted
- Date: 2026-10-22

## Decision
Object storage is accessed only through the S3 API. SeaweedFS (Apache 2.0) is used in development and self-hosted installs; any S3-compatible service works in the cloud. MinIO is not used (licence and project-direction risk); depguard blocks the MinIO client. Old EPCIS documents are archived as compressed files; OpenSearch for ad-hoc EPCIS search is optional and off for single-tenant installs.
