# One image for every mode (saas | single | hub): the mode is configuration.
FROM golang:1.26-alpine AS build
WORKDIR /src/backend
# backend/go.mod replaces the connector SDK with ../connectors-sdk.
COPY connectors-sdk/ /src/connectors-sdk/
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/openspoor/openspoor/internal/app.Version=${VERSION}" -o /out/openspoor ./cmd/openspoor

FROM node:22-alpine AS web
WORKDIR /web
RUN corepack enable
COPY frontend/ ./
RUN pnpm install --frozen-lockfile && pnpm -r build

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/openspoor /openspoor
COPY --from=web /web/apps/console/dist /web/console
COPY --from=web /web/apps/verify/dist /web/verify
COPY --from=web /web/apps/operator/dist /web/operator
ENV OPENSPOOR_HTTP_ADDR=:8080 OPENSPOOR_WEB_ROOT=/web
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/openspoor"]
CMD ["all"]
