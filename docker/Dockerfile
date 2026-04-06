FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /pbflags-server ./cmd/pbflags-server
RUN CGO_ENABLED=0 go build -o /pbflags-sync ./cmd/pbflags-sync

FROM alpine:3.23

RUN apk add --no-cache ca-certificates
COPY --from=builder /pbflags-server /usr/local/bin/pbflags-server
COPY --from=builder /pbflags-sync /usr/local/bin/pbflags-sync

EXPOSE 9200 9201

ENTRYPOINT ["pbflags-server"]
