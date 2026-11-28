FROM golang:1.26-bookworm@sha256:ea47ee45f1284d60f9cc09cb2c3bb4f508c256b640bd8bb30342ff13d1adcb52 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go catalog.json ./
RUN CGO_ENABLED=0 go build -trimpath -o /devops-server-mock .

FROM debian:bookworm-slim@sha256:e67eeead736ff7bbe258ef088bff683635c561b1fcfefad0806c1df4ab49ce0e
LABEL org.opencontainers.image.source=https://github.com/toolspell/tfsaz \
      org.opencontainers.image.description="Test image for TFSaz live tests, not for production"
# Change the date to refresh the packages: it changes the image key.
ARG REFRESH=2026-11-28
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /devops-server-mock /usr/local/bin/devops-server-mock
ENV MOCK_DATA=/data
EXPOSE 80 443 8080 8443 2222 19090
ENTRYPOINT ["devops-server-mock"]
ARG TFSAZ_LIVE_INPUTS
LABEL org.toolspell.tfsaz-live.key=$TFSAZ_LIVE_INPUTS
