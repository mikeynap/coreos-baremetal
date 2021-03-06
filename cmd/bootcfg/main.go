package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/pkg/flagutil"

	web "github.com/coreos/coreos-baremetal/bootcfg/http"
	"github.com/coreos/coreos-baremetal/bootcfg/rpc"
	"github.com/coreos/coreos-baremetal/bootcfg/server"
	"github.com/coreos/coreos-baremetal/bootcfg/sign"
	"github.com/coreos/coreos-baremetal/bootcfg/storage"
	"github.com/coreos/coreos-baremetal/bootcfg/tlsutil"
	"github.com/coreos/coreos-baremetal/bootcfg/version"
)

var (
	// Defaults to info logging
	log = logrus.New()
)

func main() {
	flags := struct {
		address     string
		rpcAddress  string
		dataPath    string
		assetsPath  string
		logLevel    string
		certFile    string
		keyFile     string
		caFile      string
		keyRingPath string
		version     bool
		help        bool
	}{}
	flag.StringVar(&flags.address, "address", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&flags.rpcAddress, "rpc-address", "", "RPC listen address")
	flag.StringVar(&flags.dataPath, "data-path", "/var/lib/bootcfg", "Path to data directory")
	flag.StringVar(&flags.assetsPath, "assets-path", "/var/lib/bootcfg/assets", "Path to static assets")

	// Log levels https://github.com/Sirupsen/logrus/blob/master/logrus.go#L36
	flag.StringVar(&flags.logLevel, "log-level", "info", "Set the logging level")

	// gRPC Server TLS
	flag.StringVar(&flags.certFile, "cert-file", "/etc/bootcfg/server.crt", "Path to the server TLS certificate file")
	flag.StringVar(&flags.keyFile, "key-file", "/etc/bootcfg/server.key", "Path to the server TLS key file")
	// TLS Client Authentication
	flag.StringVar(&flags.caFile, "ca-file", "/etc/bootcfg/ca.crt", "Path to the CA verify and authenticate client certificates")

	// Signing
	flag.StringVar(&flags.keyRingPath, "key-ring-path", "", "Path to a private keyring file")

	// subcommands
	flag.BoolVar(&flags.version, "version", false, "print version and exit")
	flag.BoolVar(&flags.help, "help", false, "print usage and exit")

	// parse command-line and environment variable arguments
	flag.Parse()
	if err := flagutil.SetFlagsFromEnv(flag.CommandLine, "BOOTCFG"); err != nil {
		log.Fatal(err.Error())
	}
	// restrict OpenPGP passphrase to pass via environment variable only
	passphrase := os.Getenv("BOOTCFG_PASSPHRASE")

	if flags.version {
		fmt.Println(version.Version)
		return
	}

	if flags.help {
		flag.Usage()
		return
	}

	// validate arguments
	if url, err := url.Parse(flags.address); err != nil || url.String() == "" {
		log.Fatal("A valid HTTP listen address is required")
	}
	if finfo, err := os.Stat(flags.dataPath); err != nil || !finfo.IsDir() {
		log.Fatal("A valid -data-path is required")
	}
	if flags.assetsPath != "" {
		if finfo, err := os.Stat(flags.assetsPath); err != nil || !finfo.IsDir() {
			log.Fatalf("Provide a valid -assets-path or '' to disable asset serving: %s", flags.assetsPath)
		}
	}
	if flags.rpcAddress != "" {
		if _, err := os.Stat(flags.certFile); err != nil {
			log.Fatalf("Provide a valid TLS server certificate with -cert-file: %v", err)
		}
		if _, err := os.Stat(flags.keyFile); err != nil {
			log.Fatalf("Provide a valid TLS server key with -key-file: %v", err)
		}
		if _, err := os.Stat(flags.caFile); err != nil {
			log.Fatalf("Provide a valid TLS certificate authority for authorizing client certificates: %v", err)
		}
	}

	// logging setup
	lvl, err := logrus.ParseLevel(flags.logLevel)
	if err != nil {
		log.Fatalf("invalid log-level: %v", err)
	}
	log.Level = lvl

	// (optional) signing
	var signer, armoredSigner sign.Signer
	if flags.keyRingPath != "" {
		entity, err := sign.LoadGPGEntity(flags.keyRingPath, passphrase)
		if err != nil {
			log.Fatal(err)
		}
		signer = sign.NewGPGSigner(entity)
		armoredSigner = sign.NewArmoredGPGSigner(entity)
	}

	// storage
	store := storage.NewFileStore(&storage.Config{
		Root: flags.dataPath,
	})

	// core logic
	server := server.NewServer(&server.Config{
		Store: store,
	})

	// gRPC Server (feature disabled by default)
	if flags.rpcAddress != "" {
		log.Infof("starting bootcfg gRPC server on %s", flags.rpcAddress)
		log.Infof("Using TLS server certificate: %s", flags.certFile)
		log.Infof("Using TLS server key: %s", flags.keyFile)
		log.Infof("Using CA certificate: %s to authenticate client certificates", flags.caFile)
		lis, err := net.Listen("tcp", flags.rpcAddress)
		if err != nil {
			log.Fatalf("failed to start listening: %v", err)
		}
		tlsinfo := tlsutil.TLSInfo{
			CertFile: flags.certFile,
			KeyFile:  flags.keyFile,
			CAFile:   flags.caFile,
		}
		tlscfg, err := tlsinfo.ServerConfig()
		if err != nil {
			log.Fatalf("Invalid TLS credentials: %v", err)
		}
		grpcServer := rpc.NewServer(server, tlscfg)
		go grpcServer.Serve(lis)
		defer grpcServer.Stop()
	}

	// HTTP Server
	config := &web.Config{
		Core:          server,
		Logger:        log,
		AssetsPath:    flags.assetsPath,
		Signer:        signer,
		ArmoredSigner: armoredSigner,
	}
	httpServer := web.NewServer(config)
	log.Infof("starting bootcfg HTTP server on %s", flags.address)
	err = http.ListenAndServe(flags.address, httpServer.HTTPHandler())
	if err != nil {
		log.Fatalf("failed to start listening: %v", err)
	}
}
