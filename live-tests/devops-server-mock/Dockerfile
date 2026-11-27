FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go catalog.json ./
RUN CGO_ENABLED=0 go build -trimpath -o /devops-server-mock .

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /devops-server-mock /usr/local/bin/devops-server-mock
ENV MOCK_DATA=/data
EXPOSE 80 443 8080 8443 2222 19090
ENTRYPOINT ["devops-server-mock"]
