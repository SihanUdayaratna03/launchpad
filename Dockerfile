# Build stage
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/controller ./cmd/controller

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/controller /controller

USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/controller"]

