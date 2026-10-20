FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/mantik ./cmd/mantik
# Retain the pinned runtime closure for the fixed no-import linker profile.
RUN mkdir -p /compiler/cache && CGO_ENABLED=0 go list -deps -export -f '{{if .Export}}{{.ImportPath}} {{.Export}}{{end}}' runtime > /tmp/exports && \
    while read -r name archive; do if [ -n "$archive" ]; then file="/compiler/cache/$(basename "$archive")"; cp "$archive" "$file"; printf 'packagefile %s=%s\n' "$name" "$file" >> /compiler/runtime.cfg; fi; done < /tmp/exports

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
WORKDIR /app
COPY --from=build /out/mantik /app/mantik
COPY --from=build /usr/local/go/pkg/tool/linux_amd64/compile /compiler/compile
COPY --from=build /usr/local/go/pkg/tool/linux_amd64/link /compiler/link
COPY --from=build /compiler/cache /compiler/cache
COPY --from=build /compiler/runtime.cfg /compiler/runtime.cfg
USER 65532:65532
ENTRYPOINT ["/app/mantik"]
