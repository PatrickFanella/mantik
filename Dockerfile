FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN go build -o /out/worker ./cmd/worker

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/worker /app/worker
CMD ["/app/worker"]
