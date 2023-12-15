package main

import (
	"archive/tar"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	log "k8s.io/klog/v2"

	containerdv1 "github.com/containerd/containerd/pkg/cri/config"
	containerdv2 "github.com/containerd/containerd/v2/services/server/config"
	"github.com/klauspost/compress/gzip"
	"github.com/pelletier/go-toml/v2"

	"github.com/taigrr/systemctl"
)

func main() {
	ctx := context.Background()

	// get parameters through flags
	configPath := flag.String("config-path", "/etc/containerd/config.toml", "path to containerd config.toml")
	shimDir := flag.String("shim-dir", "/usr/local/bin", "path to install the shim to")
	name := flag.String("name", "", "name of the shim")
	url := flag.String("url", "", "http URL of the .tar.gz shim")
	binary := flag.String("binary", "", "name of the shim binary in the root of the uncompressed tar.gz in the url")
	flag.Parse()

	// validate flags
	if configPath == nil || *configPath == "" {
		panic(fmt.Errorf("--config-path is required"))
	}
	if shimDir == nil || *shimDir == "" {
		panic(fmt.Errorf("--shim-dir is required"))
	}
	if name == nil || *name == "" {
		panic(fmt.Errorf("--name is required"))
	}
	if url == nil || *url == "" {
		panic(fmt.Errorf("--url is required"))
	}
	if binary == nil || *binary == "" {
		panic(fmt.Errorf("--binary is required"))
	}

	shimPath := filepath.Join(*shimDir, *name)
	relativeShimPath, err := filepath.Rel(*configPath, shimPath)
	if err != nil {
		panic(fmt.Errorf("calculating relative shim path: %w", err))
	}

	// TODO: provide way of cleaning up shim, maybe a cleanup flag that will reverse the following steps?
	// TODO: need a way of providing versioning. How to handle upgrade scenarios?
	// TODO: move all these to nice divided packages / functions and clean this all up. This is just a proof of concept right now.

	// download shim from url
	targz, err := http.Get(*url)
	if err != nil {
		panic(fmt.Errorf("downloading shim from %s: %w", *url, err))
	}

	uncompressed, err := gzip.NewReader(targz.Body)
	if err != nil {
		panic(fmt.Errorf("creating gzip reader: %w", err))
	}
	reader := tar.NewReader(uncompressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			panic(fmt.Errorf("shim %s not found in %s", *binary, *url))
		}
		if err != nil {
			panic(fmt.Errorf("reading tar header: %w", err))
		}

		if header.Typeflag == tar.TypeReg && header.Name == *binary {
			if err := os.MkdirAll(filepath.Dir(*shimDir), 0755); err != nil {
				panic(fmt.Errorf("ensuring shim directory: %w", err))
			}

			shimFile, err := os.Create(shimPath)
			if err != nil {
				panic(fmt.Errorf("creating shim file: %w", err))
			}
			defer shimFile.Close()

			if _, err := io.Copy(shimFile, reader); err != nil {
				panic(fmt.Errorf("writing shim file: %w", err))
			}

			break
		}
	}

	// add shim to containerd config
	if err := os.MkdirAll(filepath.Dir(*configPath), 0755); err != nil {
		panic(fmt.Errorf("ensuring containerd config directory: %w", err))
	}

	log.Infof("loading containerd config from %s", *configPath)
	cfg := containerdv2.Config{}
	if err := containerdv2.LoadConfig(ctx, *configPath, &cfg); err != nil && !os.IsNotExist(err) {
		panic(fmt.Errorf("loading containerd config: %w", err))
	}
	log.Infof("loaded containerd config from %s", *configPath)

	log.Infof("adding shim %s to %s", *name, *shimDir)
	if cfg.Plugins == nil {
		cfg.Plugins = make(map[string]interface{})
	}
	criPlugins, ok := cfg.Plugins["io.containerd.grpc.v1.cri"].(map[string]interface{})
	if !ok {
		criPlugins = make(map[string]interface{})
		cfg.Plugins["io.containerd.grpc.v1.cri"] = criPlugins
	}
	containerdPlugins, ok := criPlugins["containerd"].(map[string]interface{})
	if !ok {
		containerdPlugins = make(map[string]interface{})
		criPlugins["containerd"] = containerdPlugins
	}
	runtimes, ok := containerdPlugins["runtimes"].(map[string]interface{})
	if !ok {
		runtimes = make(map[string]interface{})
		containerdPlugins["runtimes"] = runtimes
	}

	runtimes[*name] = containerdv1.Runtime{
		Type: *name,
		Path: relativeShimPath,
	}

	cfgFile, err := os.Create(*configPath)
	if err != nil {
		panic(fmt.Errorf("opening containerd config file: %w", err))
	}
	defer cfgFile.Close()

	log.Infof("saving updated containerd config to %s", *configPath)
	if err := toml.NewEncoder(cfgFile).SetIndentTables(true).Encode(cfg); err != nil {
		panic(fmt.Errorf("encoding containerd config: %w", err))
	}
	log.Infof("saved updated containerd config to %s", *configPath)

	// restart containerd
	log.Info("restarting containerd")
	if err := systemctl.Restart(ctx, "containerd", systemctl.Options{UserMode: false}); err != nil {
		panic(fmt.Errorf("restarting containerd: %w", err))
	}
	log.Info("restarted containerd")

	// TODO: verify containerd is running correctly now.
	// TODO: if containerd isn't running correctly or there's any issues we need to revert the install and edit to containerd.toml config
}
