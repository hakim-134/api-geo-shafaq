# One image, three commands: shafaq-api, shafaq-worker, shafaq-tools (see docker-compose.yml).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN for cmd in api worker tools; do \
      CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shafaq-$cmd ./cmd/$cmd || exit 1; \
    done

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H app
COPY --from=build /out/ /usr/local/bin/
USER app
ARG VERSION=dev
ENV VERSION=$VERSION
EXPOSE 8131
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8131/v1/health || exit 1
CMD ["shafaq-api"]
