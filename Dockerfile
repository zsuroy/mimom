FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o mimom ./cmd/mimom/

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=build /app/mimom /usr/local/bin/
COPY config.yaml /etc/mimom/config.yaml
CMD ["mimom", "-config", "/etc/mimom/config.yaml"]
