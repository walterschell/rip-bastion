//go:build rpi

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/walterschell/rip-bastion/internal/display"
	"github.com/walterschell/rip-bastion/internal/drm"
	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/proxy"
	"github.com/walterschell/rip-bastion/internal/spi"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

func printInstallCmd() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("Could not determine executable path: %v", err)
	}
	fmt.Printf(`sudo tee /etc/systemd/system/rip-bastion.service << 'EOF'
`+serviceUnit+`EOF
sudo systemctl daemon-reload
sudo systemctl enable rip-bastion
sudo systemctl start rip-bastion
`, exe)
}

const serviceUnit = `[Unit]
Description=RIP Bastion Display Service

[Service]
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

const servicePath = "/etc/systemd/system/rip-bastion.service"

func install() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("Could not determine executable path: %v", err)
	}

	content := fmt.Sprintf(serviceUnit, exe)
	if err := os.WriteFile(servicePath, []byte(content), 0644); err != nil {
		log.Fatalf("Failed to write service file: %v", err)
	}
	fmt.Printf("Wrote %s\n", servicePath)

	for _, args := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "rip-bastion"},
		{"systemctl", "start", "rip-bastion"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("Command %v failed: %v", args, err)
		}
		fmt.Printf("OK: %v\n", args)
	}
}

func setSPIRuntimeDefaults() {
	if _, ok := os.LookupEnv("RIP_SPI_STREAM_FULL_FRAME"); !ok {
		_ = os.Setenv("RIP_SPI_STREAM_FULL_FRAME", "1")
	}
	if _, ok := os.LookupEnv("RIP_SPI_COLUMN_MAJOR"); !ok {
		_ = os.Setenv("RIP_SPI_COLUMN_MAJOR", "0")
	}
	if _, ok := os.LookupEnv("RIP_SPI_PACK_PIXELS16"); !ok {
		_ = os.Setenv("RIP_SPI_PACK_PIXELS16", "0")
	}
}

const defaultProxyConfig = proxy.DefaultConfigPath

func main() {
	installCmd := flag.Bool("install-cmd", false, "Print a bash command to install a systemd service and exit")
	installFlag := flag.Bool("install", false, "Install and start a systemd service for rip-bastion")
	proxyConfig := flag.String("proxy-config", defaultProxyConfig, "Path to proxy YAML config file (empty string disables the proxy)")
	writeProxyConfig := flag.Bool("write-proxy-config", false, "Write a documented placeholder proxy config to --proxy-config and print it")
	overwriteProxyConfig := flag.Bool("overwrite-proxy-config", false, "Overwrite existing --proxy-config when used with --write-proxy-config")
	flag.Parse()

	if *installCmd {
		printInstallCmd()
		return
	}
	if *installFlag {
		install()
		return
	}
	if *writeProxyConfig {
		created, err := proxy.WritePlaceholderConfig(*proxyConfig, *overwriteProxyConfig)
		if err != nil {
			log.Fatalf("proxy: writing placeholder config failed: %v", err)
		}
		if created {
			fmt.Printf("Wrote placeholder proxy config: %s\n\n", *proxyConfig)
		} else {
			fmt.Printf("Proxy config already exists (use --overwrite-proxy-config to replace): %s\n\n", *proxyConfig)
		}
		data, err := os.ReadFile(*proxyConfig)
		if err != nil {
			log.Fatalf("proxy: reading config for display failed: %v", err)
		}
		fmt.Printf("----- %s -----\n%s\n", *proxyConfig, string(data))
		return
	}

	setSPIRuntimeDefaults()

	var dev display.Device
	var err error

	dev, err = spi.NewFromEnv()
	if err != nil {
		log.Printf("SPI display init failed (%v), falling back to DRM", err)
		dev, err = drm.NewAuto()
		if err != nil {
			log.Fatalf("Failed to open display (SPI and DRM both failed): %v", err)
		}
	}

	sd := display.NewSystemDisplay(dev)
	defer sd.Close()

	msgStore := messages.NewStore()
	msgStore.Add("System starting...")
	proxyConfigForCollector := ""
	if *proxyConfig != "" {
		if _, statErr := os.Stat(*proxyConfig); statErr == nil {
			proxyConfigForCollector = *proxyConfig
		}
	}

	collector := sysinfo.NewCollectorWithProxyConfig(vpn.DefaultProvider(), msgStore, proxyConfigForCollector)
	defer collector.Stop()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Start the built-in reverse proxy if a config file is present.
	if *proxyConfig != "" {
		if _, statErr := os.Stat(*proxyConfig); statErr == nil {
			proxySrv, err := proxy.NewServer(*proxyConfig)
			if err != nil {
				log.Printf("proxy: failed to initialise (proxy disabled): %v", err)
			} else {
				msgStore.Add("Proxy starting...")
				go func() {
					if err := proxySrv.ListenAndServeContext(ctx); err != nil {
						log.Printf("proxy: server exited: %v", err)
					}
				}()
			}
		} else {
			log.Printf("proxy: config file %q not found – proxy disabled", *proxyConfig)
		}
	}

	renderCurrent := func() {
		snap := collector.Collect()
		sd.Update(snap)
		if err := sd.Render(); err != nil {
			log.Printf("Render error: %v", err)
		}
	}

	renderCurrent()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := sd.RenderNeedsRebootNotice(); err != nil {
				log.Printf("Shutdown render error: %v", err)
			}
			return
		case <-ticker.C:
			renderCurrent()
		}
	}
}
