# Dockerfile for pogod user container (cloud mode)
# Runs a user's pogo instance in Fargate.

FROM golang:1.25-alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w" \
    -o /pogod ./cmd/pogod

# -----------------------------------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates git tini

RUN addgroup -S pogo && adduser -S pogo -G pogo

# Agent attach sockets used to live in /tmp/pogo-agents, pre-created here. Since
# mg-8532 they hang off PogoHome() (POGO_HOME, else $HOME/.pogo), which pogod
# creates itself at startup — as it already did for the rest of the state tree.
RUN mkdir -p /workspace/repos && \
    chown -R pogo:pogo /workspace

COPY --from=builder /pogod /usr/local/bin/pogod

USER pogo

ENV POGO_MODE=cloud \
    POGO_BIND=0.0.0.0 \
    POGO_PORT=10000 \
    POGO_WORKSPACE_DIR=/workspace/repos

EXPOSE 10000

ENTRYPOINT ["tini", "--"]
CMD ["pogod", "--bind", "0.0.0.0", "--port", "10000"]
