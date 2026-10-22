module github.com/openspoor/openspoor

go 1.26.0

require (
	github.com/MicahParks/keyfunc/v3 v3.8.2
	github.com/RoaringBitmap/roaring/v2 v2.30.0
	github.com/boombuler/barcode v1.1.0
	github.com/capitalone/fpe v1.2.1
	github.com/go-chi/chi/v5 v5.5.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/pressly/goose/v3 v3.29.0
	github.com/prometheus/client_golang v1.25.0
	github.com/twmb/franz-go v1.22.2
	golang.org/x/crypto v0.58.0
)

require (
	github.com/Masterminds/semver/v3 v3.1.1 // indirect
	github.com/Unleash/unleash-client-go/v4 v4.1.4 // indirect
	github.com/open-feature/go-sdk v1.19.0 // indirect
	github.com/open-feature/go-sdk-contrib/providers/unleash v0.1.1-alpha // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/twmb/murmur3 v1.1.8 // indirect
	go.uber.org/mock v0.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/MicahParks/jwkset v0.11.3 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/openspoor/openspoor/connectors-sdk v0.0.0
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/openspoor/openspoor/connectors-sdk => ../connectors-sdk
