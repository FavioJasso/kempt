# syntax=docker/dockerfile:1

# Build a static binary (no CGO: every dependency is pure Go).
FROM golang:1.27-alpine AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kempt ./cmd/kempt

# Distroless static: no shell, no libc, runs as an unprivileged user.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kempt /usr/local/bin/kempt
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/kempt"]
CMD ["serve"]
