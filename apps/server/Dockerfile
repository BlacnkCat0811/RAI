FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /rai ./cmd/rai

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /rai /rai
USER 65532:65532
ENV RAI_LISTEN_ADDR=0.0.0.0:8080 GOMEMLIMIT=384MiB
EXPOSE 8080
ENTRYPOINT ["/rai"]
CMD ["api"]
