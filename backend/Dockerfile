# Build on the node and load straight into k3s (no registry):
#
#   docker build -t pulse:0.2.2 backend/
#   docker save pulse:0.2.2 | sudo k3s ctr images import -
#
# Building on the Mac (arm64) needs --platform linux/amd64.

FROM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure Go (modernc SQLite), so the binary is static and runs on distroless.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pulse ./cmd/pulse

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pulse /pulse
USER nonroot:nonroot
ENTRYPOINT ["/pulse"]
CMD ["serve"]
