# Build stage
FROM golang:1.25.4-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -o /out/backend .


# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder --chown=nonroot:nonroot /out/backend /app/backend

ENV LISTEN_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/app/backend"]