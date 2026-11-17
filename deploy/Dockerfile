FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

RUN mkdir -p /out/data && touch /out/data/.keep
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/oc-remote-relay ./cmd/oc-remote-relay

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=65532:65532 /out/oc-remote-relay /usr/local/bin/oc-remote-relay
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
EXPOSE 4097
ENTRYPOINT ["/usr/local/bin/oc-remote-relay"]
CMD ["serve"]
