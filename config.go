package cagent

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/troian/toml"
)

var DefaultCfgPath string

var configAutogeneratedHeadline = []byte(
	`# This is an auto-generated config to connect with the cloudradar service
# To see all options of cagent run cagent -p

`)

type MinValuableConfig struct {
	HubURL      string `toml:"hub_url" commented:"true"`
	HubUser     string `toml:"hub_user" commented:"true"`
	HubPassword string `toml:"hub_password" commented:"true"`
}

type Cagent struct {
	Interval float64 `toml:"interval" comment:"interval to push metrics to the HUB"`

	PidFile  string   `toml:"pid" comment:"pid file location"`
	LogFile  string   `toml:"log,omitempty" required:"false" comment:"log file location"`
	LogLevel LogLevel `toml:"log_level" comment:"\"debug\", \"info\", \"error\" verbose level; can be overriden with -v flag"`

	HubURL      string `toml:"hub_url" commented:"true"`
	HubUser     string `toml:"hub_user" commented:"true"`
	HubPassword string `toml:"hub_password" commented:"true"`

	HubGzip          bool   `toml:"hub_gzip" comment:"enable gzip when sending results to the HUB"`
	HubProxy         string `toml:"hub_proxy" commented:"true"`
	HubProxyUser     string `toml:"hub_proxy_user" commented:"true"`
	HubProxyPassword string `toml:"hub_proxy_password" commented:"true"`

	CPULoadDataGather []string `toml:"cpu_load_data_gathering_mode" comment:"default ['avg1']"`
	CPUUtilDataGather []string `toml:"cpu_utilisation_gathering_mode" comment:"default ['avg1']"`
	CPUUtilTypes      []string `toml:"cpu_utilisation_types" comment:"default ['user','system','idle','iowait']"`

	FSTypeInclude []string `toml:"fs_type_include" comment:"default ['ext3','ext4','xfs','jfs','ntfs','btrfs','hfs','apfs','fat32']"`
	FSPathExclude []string `toml:"fs_path_exclude" comment:"default []"`
	FSMetrics     []string `toml:"fs_metrics" comment:"default ['free_B','free_percent','total_B']"`

	NetInterfaceExclude             []string `toml:"net_interface_exclude" commented:"true"`
	NetInterfaceExcludeRegex        []string `toml:"net_interface_exclude_regex" comment:"default [], default on windows: [\"Pseudo-Interface\"]"`
	NetInterfaceExcludeDisconnected bool     `toml:"net_interface_exclude_disconnected" comment:"default true"`
	NetInterfaceExcludeLoopback     bool     `toml:"net_interface_exclude_loopback" comment:"default true"`

	NetMetrics []string `toml:"net_metrics" comment:"default['in_B_per_s', 'out_B_per_s']"`

	SystemFields []string `toml:"system_fields" comment:"default ['uname','os_kernel','os_family','os_arch','cpu_model','fqdn','memory_total_B']"`

	WindowsUpdatesWatcherInterval int `toml:"windows_updates_watcher_interval" comment:"default 3600"`

	// internal use
	hubHttpClient *http.Client

	cpuWatcher           *CPUWatcher
	fsWatcher            *FSWatcher
	netWatcher           *NetWatcher
	windowsUpdateWatcher *WindowsUpdateWatcher

	rootCAs *x509.CertPool
	version string
}

func New() *Cagent {
	var defaultLogPath string
	var rootCertsPath string

	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}
	exPath := filepath.Dir(ex)

	switch runtime.GOOS {
	case "windows":
		DefaultCfgPath = filepath.Join(exPath, "./cagent.conf")
		defaultLogPath = filepath.Join(exPath, "./cagent.log")
	case "darwin":
		DefaultCfgPath = os.Getenv("HOME") + "/.cagent/cagent.conf"
		defaultLogPath = os.Getenv("HOME") + "/.cagent/cagent.log"
	default:
		rootCertsPath = "/etc/cagent/cacert.pem"
		DefaultCfgPath = "/etc/cagent/cagent.conf"
		defaultLogPath = "/var/log/cagent/cagent.log"
	}

	ca := &Cagent{
		LogFile:                         defaultLogPath,
		Interval:                        90,
		CPULoadDataGather:               []string{"avg1"},
		CPUUtilTypes:                    []string{"user", "system", "idle", "iowait"},
		CPUUtilDataGather:               []string{"avg1"},
		FSTypeInclude:                   []string{"ext3", "ext4", "xfs", "jfs", "ntfs", "btrfs", "hfs", "apfs", "fat32"},
		FSMetrics:                       []string{"free_B", "free_percent", "total_B"},
		NetMetrics:                      []string{"in_B_per_s", "out_B_per_s"},
		NetInterfaceExcludeDisconnected: true,
		NetInterfaceExclude:             []string{},
		NetInterfaceExcludeRegex:        []string{},
		NetInterfaceExcludeLoopback:     true,
		SystemFields:                    []string{"uname", "os_kernel", "os_family", "os_arch", "cpu_model", "fqdn", "memory_total_B"},
	}

	if runtime.GOOS == "windows" {
		ca.WindowsUpdatesWatcherInterval = 3600
		ca.NetInterfaceExcludeRegex = []string{"Pseudo-Interface"}
		ca.CPULoadDataGather = []string{}
		ca.CPUUtilTypes = []string{"user", "system", "idle"}
	}

	if rootCertsPath != "" {
		if _, err := os.Stat(rootCertsPath); err == nil {
			certPool := x509.NewCertPool()

			b, err := ioutil.ReadFile(rootCertsPath)
			if err != nil {
				log.Error("Failed to read cacert.pem: ", err.Error())
			} else {
				ok := certPool.AppendCertsFromPEM(b)
				if ok {
					ca.rootCAs = certPool
				}
			}
		}
	}

	return ca
}

func secToDuration(secs float64) time.Duration {
	return time.Duration(int64(float64(time.Second) * secs))
}

func (mvc *MinValuableConfig) ApplyEnv() {
	if val, ok := os.LookupEnv("CAGENT_HUB_URL"); ok {
		mvc.HubURL = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_USER"); ok {
		mvc.HubUser = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_PASSWORD"); ok {
		mvc.HubPassword = val
	}
}

// todo: merge it with (ca *MinValuableConfig) ApplyEnv() when config separated from Cagent
func (ca *Cagent) ApplyEnv() {
	if val, ok := os.LookupEnv("CAGENT_HUB_URL"); ok {
		ca.HubURL = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_USER"); ok {
		ca.HubUser = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_PASSWORD"); ok {
		ca.HubPassword = val
	}
}

func (ca *Cagent) SetVersion(version string) {
	ca.version = version
}

func (ca *Cagent) userAgent() string {
	if ca.version == "" {
		ca.version = "{undefined}"
	}
	parts := strings.Split(ca.version, "-")

	return fmt.Sprintf("Cagent v%s %s %s", parts[0], runtime.GOOS, runtime.GOARCH)
}

func (ca *Cagent) DumpConfigToml() string {
	buff := &bytes.Buffer{}

	err := toml.NewEncoder(buff).Encode(ca)
	if err != nil {
		log.Errorf("DumpConfigToml error: %s", err.Error())
		return ""
	}

	return buff.String()
}

func (ca *Cagent) ReadConfigFromFile(configFilePath string) error {
	_, err := os.Stat(configFilePath)
	if err != nil {
		return err
	}

	_, err = toml.DecodeFile(configFilePath, &ca)

	return err
}

func CreateDefaultConfigFile(configFilePath string) error {
	var err error

	if _, err = os.Stat(configFilePath); os.IsExist(err) {
		return fmt.Errorf("config already exists at path: %s", configFilePath)
	}

	var f *os.File
	if f, err = os.OpenFile(configFilePath, os.O_WRONLY|os.O_CREATE, 0644); err != nil {
		return fmt.Errorf("failed to create the default config file: '%s'", configFilePath)
	}

	defer func() {
		if err = f.Close(); err != nil {
			log.WithError(err).Errorf("failed to close config file: %s", configFilePath)
		}
	}()

	if _, err = f.Write(configAutogeneratedHeadline); err != nil {
		return fmt.Errorf("failed to write headline to config file")
	}

	var cfg MinValuableConfig

	cfg.ApplyEnv()

	err = toml.NewEncoder(f).Encode(&cfg)
	if err != nil {
		return fmt.Errorf("failed to encode config to file")
	}

	log.Infof("generated minimum valuable config to %s", configFilePath)

	return err
}

func (ca *Cagent) Initialize() error {
	if ca.HubProxy != "" {
		if !strings.HasPrefix(ca.HubProxy, "http") {
			ca.HubProxy = "http://" + ca.HubProxy
		}
		_, err := url.Parse(ca.HubProxy)
		if err != nil {
			return fmt.Errorf("Failed to parse 'hub_proxy' URL")
		}
	}

	ca.SetLogLevel(ca.LogLevel)

	if ca.LogFile != "" {
		err := addLogFileHook(ca.LogFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			log.Error("Can't write logs to file: ", err.Error())
		}
	}

	return nil
}

// HandleConfig configures Cagent with parameters specified in file
// if config file not exists default one created in form of MinValuableConfig
// todo: this function must be removed once config is separated from Cagent
func HandleConfig(ca *Cagent, configFilePath string) error {
	err := ca.ReadConfigFromFile(configFilePath)
	if os.IsNotExist(err) {
		// this is ok
		err = CreateDefaultConfigFile(configFilePath)
		if err != nil {
			log.Fatal(err)
		}
	} else if err != nil {
		if strings.Contains(err.Error(), "cannot load TOML value of type int64 into a Go float") {
			log.Fatalf("Config load error: please use numbers with a decimal point for numerical values")
		} else {
			log.Fatalf("Config load error: %s", err.Error())
		}
	}

	ca.ApplyEnv()

	return err
}
