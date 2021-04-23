
#! /bin/bash

export TMPDIR=/home/work/go/tmp
export GO111MODULE=on
export GOPROXY=https://goproxy.io
export GOPATH=/home/work/go
export GOROOT=/home/work/local/go
export GOBIN=/home/work/go/bin

# delete reg socket if exist
rm -rf /var/lib/kubelet/plugins_registry/fuse.csi.fluid.io-reg.sock
/home/work/local/go/bin/go run main.go \
	--kubelet-registration-path="/var/lib/kubelet/csi-plugins/fuse.csi.fluid.io/csi.sock" \
	--csi-address="/var/lib/kubelet/csi-plugins/fuse.csi.fluid.io/csi.sock" \
    --reg-path="/var/lib/kubelet/plugins_registry" \
    --v=5
