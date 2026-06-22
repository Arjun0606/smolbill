# Single static binary, Postgres-only (build plan §10). Multi-stage so the final
# image is tiny and contains nothing but the engine.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/smolbill ./cmd/smolbill

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/smolbill /smolbill
EXPOSE 8080
ENTRYPOINT ["/smolbill", "serve"]
