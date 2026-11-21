# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/master2sid2/cert-alerter/internal/version.Version=${VERSION}" \
      -o /out/cert-alerter ./cmd/cert-alerter \
 && mkdir -p /out/data

# distroless/static ships CA certificates and runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cert-alerter /usr/local/bin/cert-alerter
COPY --from=build --chown=65532:65532 /out/data /data
ENV CERT_ALERTER_DATA_DIR=/data \
    CERT_ALERTER_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s CMD ["/usr/local/bin/cert-alerter", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cert-alerter"]
CMD ["serve"]
