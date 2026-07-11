FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vessica-knowledge-server ./cmd/vessica-knowledge-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vessica-knowledge-server /vessica-knowledge-server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/vessica-knowledge-server"]
