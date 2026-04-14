FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /pbflags-admin ./cmd/pbflags-admin
RUN CGO_ENABLED=0 go build -o /pbflags-evaluator ./cmd/pbflags-evaluator
RUN CGO_ENABLED=0 go build -o /pbflags ./cmd/pbflags

FROM alpine:3.23

RUN apk add --no-cache ca-certificates
COPY --from=builder /pbflags-admin /usr/local/bin/pbflags-admin
COPY --from=builder /pbflags-evaluator /usr/local/bin/pbflags-evaluator
COPY --from=builder /pbflags /usr/local/bin/pbflags

EXPOSE 9200 9201

ENTRYPOINT ["pbflags-admin"]
