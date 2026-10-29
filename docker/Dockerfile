# RKA platform image.
#
# Node is only used to build the browser bundle; it is not part of the runtime.
# The runtime image is postgres:17-alpine because the backup and restore
# commands need pg_dump/pg_restore matching the database major version.

FROM --platform=$BUILDPLATFORM node:22.23.3-alpine AS web
WORKDIR /build
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/index.html frontend/tsconfig.json frontend/vite.config.ts ./
COPY frontend/src ./src
COPY frontend/public ./public
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS gobuild
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/rka ./cmd/rka

FROM postgres:17.6-alpine AS runtime
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=gobuild /out/rka /usr/local/bin/rka
COPY --from=web /build/dist ./dist
COPY content ./content
COPY LICENSE ./LICENSE
COPY docker/rka.env.example ./deploy/rka.env.example
ENV RKA_ADDR=0.0.0.0:8080 \
    RKA_STATIC_DIR=/app/dist \
    RKA_CONTENT_SNAPSHOT=/app/content \
    RKA_DATA_DIR=/data
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=12 \
  CMD wget -q -O- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["rka"]
CMD ["serve"]
