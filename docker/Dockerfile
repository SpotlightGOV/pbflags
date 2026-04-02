FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /pbflags-server ./cmd/pbflags-server

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=builder /pbflags-server /usr/local/bin/pbflags-server

EXPOSE 9200 9201

ENTRYPOINT ["pbflags-server"]
