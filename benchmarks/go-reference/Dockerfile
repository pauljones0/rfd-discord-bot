FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /rfd-bot ./cmd/rfd

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /rfd-bot /rfd-bot
COPY --chown=65532:65532 data/.keep /data/.keep
USER 65532:65532
WORKDIR /data
ENV SQLITE_PATH=/data/rfd.sqlite LISTEN_ADDR=127.0.0.1:8080
ENTRYPOINT ["/rfd-bot"]
CMD ["run"]
