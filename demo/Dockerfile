FROM golang:1.26-alpine AS build

ARG COMMIT=dev
WORKDIR /src
COPY go.mod ./
COPY demo/ ./demo/
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.commit=${COMMIT}" -o /out/demo ./demo

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/demo /demo

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/demo"]
