package main

import (
	"fmt"

	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"os"

	"github.com/Shopify/sarama"
	"github.com/spf13/cobra"

	"github.com/birdayz/kaf/pkg/avro"
	"github.com/birdayz/kaf/pkg/config"
	"github.com/birdayz/kaf/pkg/proto"
)

var cfgFile string

func getConfig() (saramaConfig *sarama.Config) {
	saramaConfig = sarama.NewConfig()
	// Use an old version of the protocol by default, for widest support
	saramaConfig.Version = sarama.V0_10_0_0
	saramaConfig.Producer.Return.Successes = true

	cluster := currentCluster

	clusterVersion := ""
	if versionOverride != "" {
		clusterVersion = versionOverride
	} else if cluster.Version != "" {
		clusterVersion = cluster.Version
	}

	if clusterVersion != "" {
		parsedVersion, err := sarama.ParseKafkaVersion(clusterVersion)
		if err != nil {
			errorExit("Unable to parse Kafka version: %v\n", err)
		}
		saramaConfig.Version = parsedVersion
	}
	if cluster.SASL != nil {
		saramaConfig.Net.SASL.Enable = true
		saramaConfig.Net.SASL.User = cluster.SASL.Username
		saramaConfig.Net.SASL.Password = cluster.SASL.Password
	}
	if cluster.TLS != nil && cluster.SecurityProtocol != "SASL_SSL" {
		saramaConfig.Net.TLS.Enable = true
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cluster.TLS.Insecure,
		}

		if cluster.TLS.Cafile != "" {
			caCert, err := ioutil.ReadFile(cluster.TLS.Cafile)
			if err != nil {
				errorExit("Unable to read Cafile :%v\n", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = caCertPool
		}

		if cluster.TLS.Clientfile != "" && cluster.TLS.Clientkeyfile != "" {
			clientCert, err := ioutil.ReadFile(cluster.TLS.Clientfile)
			if err != nil {
				errorExit("Unable to read Clientfile :%v\n", err)
			}
			clientKey, err := ioutil.ReadFile(cluster.TLS.Clientkeyfile)
			if err != nil {
				errorExit("Unable to read Clientkeyfile :%v\n", err)
			}

			cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
			if err != nil {
				errorExit("Unable to creatre KeyPair: %v\n", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}

			// nolint
			tlsConfig.BuildNameToCertificate()
		}
		saramaConfig.Net.TLS.Config = tlsConfig
	}
	if cluster.SecurityProtocol == "SASL_SSL" {
		saramaConfig.Net.TLS.Enable = true
		if cluster.TLS != nil {
			tlsConfig := &tls.Config{
				InsecureSkipVerify: cluster.TLS.Insecure,
			}
			if cluster.TLS.Cafile != "" {
				caCert, err := ioutil.ReadFile(cluster.TLS.Cafile)
				if err != nil {
					fmt.Println(err)
					os.Exit(1)
				}
				caCertPool := x509.NewCertPool()
				caCertPool.AppendCertsFromPEM(caCert)
				tlsConfig.RootCAs = caCertPool
			}
			saramaConfig.Net.TLS.Config = tlsConfig

		} else {
			saramaConfig.Net.TLS.Config = &tls.Config{InsecureSkipVerify: false}
		}
		if cluster.SASL.Mechanism == "SCRAM-SHA-512" {
			saramaConfig.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
			saramaConfig.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA512)
		} else if cluster.SASL.Mechanism == "SCRAM-SHA-256" {
			saramaConfig.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
			saramaConfig.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA256)
		}
	}
	return saramaConfig
}

var rootCmd = &cobra.Command{
	Use:                    "kaf",
	Short:                  "Kafka Command Line utility for cluster management",
	BashCompletionFunction: bashCompletion,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var cfg config.Config
var currentCluster *config.Cluster

var (
	brokersFlag       []string
	schemaRegistryURL string
	protoFiles        []string
	protoExclude      []string
	verbose           bool
	jsonFlag          bool
	clusterOverride   string
	versionOverride   string
)

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.kaf/config)")
	rootCmd.PersistentFlags().StringSliceVarP(&brokersFlag, "brokers", "b", nil, "Comma separated list of broker ip:port pairs")
	rootCmd.PersistentFlags().StringVar(&schemaRegistryURL, "schema-registry", "", "URL to a Confluent schema registry. Used for attempting to decode Avro-encoded messages")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Whether to turn on sarama logging")
	rootCmd.PersistentFlags().BoolVar(&jsonFlag, "json", false, "Whether to print results as JSON")
	rootCmd.PersistentFlags().StringVarP(&clusterOverride, "cluster", "c", "", "set a temporary current cluster")
	rootCmd.PersistentFlags().StringVar(&versionOverride, "cluster-version", "", "override the kafka cluster version")
	cobra.OnInitialize(onInit)
}

var setupProtoDescriptorRegistry = func(cmd *cobra.Command, args []string) {
	if protoType != "" {
		r, err := proto.NewDescriptorRegistry(protoFiles, protoExclude)
		if err != nil {
			errorExit("Failed to load protobuf files: %v\n", err)
		}
		reg = r
	}
}

func onInit() {
	var err error
	cfg, err = config.ReadConfig(cfgFile)
	if err != nil {
		errorExit("Invalid config: %v", err)
	}

	cfg.ClusterOverride = clusterOverride

	cluster := cfg.ActiveCluster()
	if cluster != nil {
		// Use active cluster from config
		currentCluster = cluster
	} else {
		// Create sane default if not configured
		currentCluster = &config.Cluster{
			Brokers: []string{"localhost:9092"},
		}
	}

	// Any set flags override the configuration
	if schemaRegistryURL != "" {
		currentCluster.SchemaRegistryURL = schemaRegistryURL
	}

	if brokersFlag != nil {
		currentCluster.Brokers = brokersFlag
	}

	if verbose {
		sarama.Logger = log.New(os.Stderr, "[sarama] ", log.Lshortfile|log.LstdFlags)
	}
}

func getClusterAdmin() (admin sarama.ClusterAdmin) {
	clusterAdmin, err := sarama.NewClusterAdmin(currentCluster.Brokers, getConfig())
	if err != nil {
		errorExit("Unable to get cluster admin: %v\n", err)
	}

	return clusterAdmin
}

func getClient() (client sarama.Client) {
	client, err := sarama.NewClient(currentCluster.Brokers, getConfig())
	if err != nil {
		errorExit("Unable to get client: %v\n", err)
	}
	return client
}

func getSchemaCache() (cache *avro.SchemaCache) {
	if currentCluster.SchemaRegistryURL == "" {
		return nil
	}
	cache, err := avro.NewSchemaCache(currentCluster.SchemaRegistryURL)
	if err != nil {
		errorExit("Unable to get schema cache :%v\n", err)
	}
	return cache
}

func errorExit(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
