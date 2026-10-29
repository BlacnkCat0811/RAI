ARG GO_IMAGE=golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb
FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG SOURCE_REVISION=unrecorded
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.sourceCommit=${SOURCE_REVISION}" -o /rai ./cmd/rai

FROM scratch
ARG SOURCE_REVISION=unrecorded
LABEL org.opencontainers.image.source="RAI V2 apps/server" org.opencontainers.image.revision="${SOURCE_REVISION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /rai /rai
USER 65532:65532
ENV RAI_LISTEN_ADDR=0.0.0.0:8080 GOMEMLIMIT=384MiB
EXPOSE 8080
ENTRYPOINT ["/rai"]
CMD ["api"]
