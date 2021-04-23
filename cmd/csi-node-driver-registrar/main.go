/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/kubernetes-csi/csi-lib-utils/connection"
	csirpc "github.com/kubernetes-csi/csi-lib-utils/rpc"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"k8s.io/klog"
	registerapi "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1alpha1"
)

const (
	// Name of node annotation that contains JSON map of driver names to node
	// names
	annotationKey = "csi.volume.kubernetes.io/nodeid"

	// Default timeout of short CSI calls like GetPluginInfo
	csiTimeout = time.Second

	// Verify (and update, if needed) the node ID at this freqeuency.
	sleepDuration = 2 * time.Minute
)

// Command line flags
var (
	connectionTimeout       = flag.Duration("connection-timeout", 0, "The --connection-timeout flag is deprecated")
	csiAddress              = flag.String("csi-address", "/run/csi/socket", "Path of the CSI driver socket that the node-driver-registrar will connect to.")
	kubeletRegistrationPath = flag.String("kubelet-registration-path", "", "Path of the CSI driver socket on the Kubernetes host machine.")
	regPath                 = flag.String("reg-path", "/registration", "kubelet register path")
	showVersion             = flag.Bool("version", false, "Show version.")
	version                 = "unknown"

	// List of supported versions
	supportedVersions = []string{"1.0.0"}
)

// registrationServer is a sample plugin to work with plugin watcher
type registrationServer struct {
	driverName string
	endpoint   string
	version    []string
}

var _ registerapi.RegistrationServer = registrationServer{}

// NewregistrationServer returns an initialized registrationServer instance
func newRegistrationServer(driverName string, endpoint string, versions []string) registerapi.RegistrationServer {
	return &registrationServer{
		driverName: driverName,
		endpoint:   endpoint,
		version:    versions,
	}
}

// GetInfo is the RPC invoked by plugin watcher
func (e registrationServer) GetInfo(ctx context.Context, req *registerapi.InfoRequest) (*registerapi.PluginInfo, error) {
	klog.Infof("Received GetInfo call: %+v", req)
	return &registerapi.PluginInfo{
		Type:              registerapi.CSIPlugin,
		Name:              e.driverName,
		Endpoint:          e.endpoint,
		SupportedVersions: e.version,
	}, nil
}

func (e registrationServer) NotifyRegistrationStatus(ctx context.Context, status *registerapi.RegistrationStatus) (*registerapi.RegistrationStatusResponse, error) {
	klog.Infof("Received NotifyRegistrationStatus call: %+v", status)
	if !status.PluginRegistered {
		klog.Errorf("Registration process failed with error: %+v, restarting registration container.", status.Error)
		os.Exit(1)
	}

	return &registerapi.RegistrationStatusResponse{}, nil
}

func main() {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
	flag.Parse()

	if *kubeletRegistrationPath == "" {
		klog.Error("kubelet-registration-path is a required parameter")
		os.Exit(1)
	}

	if *showVersion {
		fmt.Println(os.Args[0], version)
		return
	}
	klog.Infof("Version: %s", version)

	if *connectionTimeout != 0 {
		klog.Warning("--connection-timeout is deprecated and will have no effect")
	}

	// Once https://github.com/container-storage-interface/spec/issues/159 is
	// resolved, if plugin does not support PUBLISH_UNPUBLISH_VOLUME, then we
	// can skip adding mapping to "csi.volume.kubernetes.io/nodeid" annotation.

	klog.V(1).Infof("Attempting to open a gRPC connection with: %q", *csiAddress)
	csiConn, err := connection.Connect(*csiAddress)
	if err != nil {
		klog.Errorf("error connecting to CSI driver: %v", err)
		os.Exit(1)
	}

	klog.V(1).Infof("Calling CSI driver to discover driver name")
	ctx, cancel := context.WithTimeout(context.Background(), csiTimeout)
	defer cancel()

	csiDriverName, err := csirpc.GetDriverName(ctx, csiConn)
	if err != nil {
		klog.Errorf("error retreiving CSI driver name: %v", err)
		os.Exit(1)
	}

	klog.V(2).Infof("CSI driver name: %q", csiDriverName)

	// Run forever
	nodeRegister(csiDriverName)
}

func nodeRegister(
	csiDriverName string,
) {
	// When kubeletRegistrationPath is specified then driver-registrar ONLY acts
	// as gRPC server which replies to registration requests initiated by kubelet's
	// pluginswatcher infrastructure. Node labeling is done by kubelet's csi code.
	registrar := newRegistrationServer(csiDriverName, *kubeletRegistrationPath, supportedVersions)

	socketPath := fmt.Sprintf("/%s/%s-reg.sock", *regPath, csiDriverName)
	if err := CleanupSocketFile(socketPath); err != nil {
		klog.Errorf("%+v", err)
		os.Exit(1)
	}

	var oldmask int
	if runtime.GOOS == "linux" {
		// Default to only user accessible socket, caller can open up later if desired
		oldmask, _ = Umask(0077)
	}

	klog.Infof("Starting Registration Server at: %s\n", socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		klog.Errorf("failed to listen on socket: %s with error: %+v", socketPath, err)
		os.Exit(1)
	}
	if runtime.GOOS == "linux" {
		Umask(oldmask)
	}
	klog.Infof("Registration Server started at: %s\n", socketPath)
	grpcServer := grpc.NewServer()
	// Registers kubelet plugin watcher api.
	registerapi.RegisterRegistrationServer(grpcServer, registrar)

	// Starts service
	if err := grpcServer.Serve(lis); err != nil {
		klog.Errorf("Registration Server stopped serving: %v", err)
		os.Exit(1)
	}
	// If gRPC server is gracefully shutdown, exit
	os.Exit(0)
}

func Umask(mask int) (int, error) {
	return unix.Umask(mask), nil
}

func CleanupSocketFile(socketPath string) error {
	fi, err := os.Stat(socketPath)
	if err == nil && (fi.Mode()&os.ModeSocket) != 0 {
		// Remove any socket, stale or not, but fall through for other files
		if err := os.Remove(socketPath); err != nil {
			return fmt.Errorf("failed to remove stale socket %s with error: %+v", socketPath, err)
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat the socket %s with error: %+v", socketPath, err)
	}
	return nil
}
