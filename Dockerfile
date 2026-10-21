# api-geo-shafaq: the live-rooms API in one small image.
#   docker build -t api-geo-shafaq .
#   docker run -d --name api-geo-shafaq --restart unless-stopped -p 127.0.0.1:8131:8131 api-geo-shafaq

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /api-geo-shafaq .

FROM alpine:3.23
RUN adduser -D -H app
COPY --from=build /api-geo-shafaq /usr/local/bin/api-geo-shafaq
USER app
EXPOSE 8131
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8131/api/health || exit 1
ENTRYPOINT ["api-geo-shafaq", "-addr", ":8131"]
