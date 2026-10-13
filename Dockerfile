FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/mantik ./cmd/mantik

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/mantik /app/mantik
USER 65532:65532
ENTRYPOINT ["/app/mantik"]
CMD ["serve"]
