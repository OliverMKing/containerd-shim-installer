FROM golang:1.21 as builder

WORKDIR /go/src/github.com/OliverMKing/containerd-shim-installer
ADD . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -a -ldflags '-extldflags "-static"' -o containerd-shim-installer

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/OliverMKing/containerd-shim-installer/containerd-shim-installer .
ENTRYPOINT [ "/containerd-shim-installer" ]
