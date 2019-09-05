package cagent

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/troian/toml"

	"github.com/cloudradar-monitoring/cagent/pkg/common"
)

const (
	IOModeFile = "file"
	IOModeHTTP = "http"

	OperationModeFull      = "full"
	OperationModeMinimal   = "minimal"
	OperationModeHeartbeat = "heartbeat"

	minIntervalValue          = 30.0
	minHeartbeatIntervalValue = 5.0

	minHubRequestTimeout = 1
	maxHubRequestTimeout = 600
)

var operationModes = []string{OperationModeFull, OperationModeMinimal, OperationModeHeartbeat}

var DefaultCfgPath string
var defaultLogPath string
var rootCertsPath string

var configAutogeneratedHeadline = []byte(
	`# This is an auto-generated config to connect with the cloudradar service
# To see all options of cagent run cagent -p

`)

type MinValuableConfig struct {
	LogLevel    LogLevel `toml:"log_level" comment:"\"debug\", \"info\", \"error\" verbose level; can be overridden with -v flag"`
	IOMode      string   `toml:"io_mode" commented:"true"`
	OutFile     string   `toml:"out_file,omitempty" comment:"output file path in io_mode=\"file\"\ncan be overridden with -o flag\non windows slash must be escaped\nfor example out_file = \"C:\\\\cagent.data.txt\""`
	HubURL      string   `toml:"hub_url" commented:"true"`
	HubUser     string   `toml:"hub_user" commented:"true"`
	HubPassword string   `toml:"hub_password" commented:"true"`
}

type LogsFilesConfig struct {
	HubFile string `toml:"hub_file,omitempty" comment:"log hub objects send to the hub"`
}

type Config struct {
	OperationMode     string  `toml:"operation_mode" comment:"operation_mode, possible values:\n\"full\": perform all checks unless disabled individually through other config option. Default.\n\"minimal\": perform just the checks for CPU utilization, CPU Load, Memory Usage, and Disk fill levels.\n\"heartbeat\": Just send the heartbeat according to the heartbeat interval.\nApplies only to io_mode = http, ignored on the command line."`
	Interval          float64 `toml:"interval" comment:"interval to push metrics to the HUB"`
	HeartbeatInterval float64 `toml:"heartbeat" comment:"send a heartbeat without metrics to the HUB every X seconds"`

	PidFile   string `toml:"pid" comment:"pid file location"`
	LogFile   string `toml:"log,omitempty" required:"false" comment:"log file location"`
	LogSyslog string `toml:"log_syslog" comment:"\"local\" for local unix socket or URL e.g. \"udp://localhost:514\" for remote syslog server"`

	MinValuableConfig

	HubGzip           bool   `toml:"hub_gzip" comment:"enable gzip when sending results to the HUB"`
	HubRequestTimeout int    `toml:"hub_request_timeout" comment:"time limit in seconds for requests made to Hub.\nThe timeout includes connection time, any redirects, and reading the response body.\nMin: 1, Max: 600. default: 30"`
	HubProxy          string `toml:"hub_proxy" commented:"true"`
	HubProxyUser      string `toml:"hub_proxy_user" commented:"true"`
	HubProxyPassword  string `toml:"hub_proxy_password" commented:"true"`

	CPULoadDataGather []string `toml:"cpu_load_data_gathering_mode" comment:"default ['avg1']"`
	CPUUtilDataGather []string `toml:"cpu_utilisation_gathering_mode" comment:"default ['avg1']"`
	CPUUtilTypes      []string `toml:"cpu_utilisation_types" comment:"default ['user','system','idle','iowait']"`

	FSTypeInclude        []string `toml:"fs_type_include" comment:"default ['ext3','ext4','xfs','jfs','ntfs','btrfs','hfs','apfs','fat32']"`
	FSPathExclude        []string `toml:"fs_path_exclude" comment:"Exclude file systems by name, disabled by default"`
	FSPathExcludeRecurse bool     `toml:"fs_path_exclude_recurse" comment:"Having fs_path_exclude_recurse = false the specified path must match a mountpoint or it will be ignored\nHaving fs_path_exclude_recurse = true the specified path can be any folder and all mountpoints underneath will be excluded"`

	FSMetrics []string `toml:"fs_metrics" comment:"default ['free_B', 'free_percent', 'total_B', 'read_B_per_s', 'write_B_per_s', 'read_ops_per_s', 'write_ops_per_s', 'inodes_used_percent']"`

	NetInterfaceExclude             []string `toml:"net_interface_exclude" commented:"true"`
	NetInterfaceExcludeRegex        []string `toml:"net_interface_exclude_regex" comment:"default [\"^vnet(.*)$\", \"^virbr(.*)$\", \"^vmnet(.*)$\", \"^vEthernet(.*)$\"]. On Windows, also \"Pseudo-Interface\" is added to list"`
	NetInterfaceExcludeDisconnected bool     `toml:"net_interface_exclude_disconnected" comment:"default true"`
	NetInterfaceExcludeLoopback     bool     `toml:"net_interface_exclude_loopback" comment:"default true"`

	NetMetrics           []string `toml:"net_metrics" comment:"default['in_B_per_s', 'out_B_per_s']"`
	NetInterfaceMaxSpeed string   `toml:"net_interface_max_speed" comment:"If the value is not specified, cagent will try to query the maximum speed of the network cards to calculate the bandwidth usage (default)\nDepending on the network card type this is not always reliable.\nSome virtual network cards, for example, report a maximum speed lower than the real speed.\nYou can set a fixed value by using <number of Bytes per second> + <K, M or G as a quantifier>.\nExamples: \"125M\" (equals 1 GigaBit), \"12.5M\" (equals 100 MegaBits), \"12.5G\" (equals 100 GigaBit)"`

	SystemFields []string `toml:"system_fields" comment:"default ['uname','os_kernel','os_family','os_arch','cpu_model','fqdn','memory_total_B']"`

	WindowsUpdatesWatcherInterval int `toml:"windows_updates_watcher_interval" comment:"default 3600"`

	VirtualMachinesStat []string `toml:"virtual_machines_stat" comment:"default ['hyper-v'], available options 'hyper-v'"`

	HardwareInventory bool `toml:"hardware_inventory" comment:"default true"`

	DiscoverAutostartingServicesOnly bool `toml:"discover_autostarting_services_only" comment:"default true"`

	CPUUtilisationAnalysis CPUUtilisationAnalysis `toml:"cpu_utilisation_analysis"`

	TemperatureMonitoring bool `toml:"temperature_monitoring" comment:"default true"`

	SMARTMonitoring bool            `toml:"smart_monitoring" comment:"Enable S.M.A.R.T monitoring of hard disks\ndefault false"`
	SMARTCtl        string          `toml:"smartctl" comment:"Path to a smartctl binary (smartctl.exe on windows, path must be escaped) version >= 7\nSee https://docs.cloudradar.io/configuring-hosts/installing-agents/troubleshoot-s.m.a.r.t-monitoring\nsmartctl = \"C:\\\\Program Files\\\\smartmontools\\\\bin\\\\smartctl.exe\"\nsmartctl = \"/usr/local/bin/smartctl\""`
	Logs            LogsFilesConfig `toml:"logs,omitempty"`
}

type CPUUtilisationAnalysis struct {
	Threshold                      float64 `toml:"threshold" comment:"target value to start the analysis" json:"threshold"`
	Function                       string  `toml:"function" comment:"threshold compare function, possible values: 'lt', 'lte', 'gt', 'gte'" json:"function"`
	Metric                         string  `toml:"metric" commend:"possible values: 'user','system','idle','iowait'" json:"metric"`
	GatheringMode                  string  `toml:"gathering_mode" comment:"should be one of values of cpu_utilisation_gathering_mode" json:"gathering_mode"`
	ReportProcesses                int     `toml:"report_processes" comment:"number of processes to return" json:"report_processes"`
	TrailingProcessAnalysisMinutes int     `toml:"trailing_process_analysis_minutes" comment:"how much time analysis will continue to perform after the CPU utilisation returns to the normal value" json:"trailing_process_analysis_minutes"`
}

func init() {
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
}

func NewConfig() *Config {
	cfg := &Config{
		LogFile:                          defaultLogPath,
		OperationMode:                    OperationModeFull,
		Interval:                         90,
		HeartbeatInterval:                15,
		HubGzip:                          true,
		HubRequestTimeout:                30,
		CPULoadDataGather:                []string{"avg1"},
		CPUUtilTypes:                     []string{"user", "system", "idle", "iowait"},
		CPUUtilDataGather:                []string{"avg1"},
		FSTypeInclude:                    []string{"ext3", "ext4", "xfs", "jfs", "ntfs", "btrfs", "hfs", "apfs", "fat32"},
		FSPathExclude:                    []string{},
		FSPathExcludeRecurse:             false,
		FSMetrics:                        []string{"free_B", "free_percent", "total_B", "read_B_per_s", "write_B_per_s", "read_ops_per_s", "write_ops_per_s"},
		NetMetrics:                       []string{"in_B_per_s", "out_B_per_s"},
		NetInterfaceExcludeDisconnected:  true,
		NetInterfaceExclude:              []string{},
		NetInterfaceExcludeRegex:         []string{"^vnet(.*)$", "^virbr(.*)$", "^vmnet(.*)$", "^vEthernet(.*)$"},
		NetInterfaceExcludeLoopback:      true,
		SystemFields:                     []string{"uname", "os_kernel", "os_family", "os_arch", "cpu_model", "fqdn", "memory_total_B"},
		HardwareInventory:                true,
		DiscoverAutostartingServicesOnly: true,
		CPUUtilisationAnalysis: CPUUtilisationAnalysis{
			Threshold:                      10,
			Function:                       "lt",
			Metric:                         "idle",
			GatheringMode:                  "avg1",
			ReportProcesses:                5,
			TrailingProcessAnalysisMinutes: 5,
		},
		SMARTMonitoring:       false,
		TemperatureMonitoring: true,
		Logs: LogsFilesConfig{
			HubFile: "",
		},
	}

	cfg.MinValuableConfig = *(defaultMinValuableConfig())

	if runtime.GOOS == "windows" {
		cfg.WindowsUpdatesWatcherInterval = 3600
		cfg.NetInterfaceExcludeRegex = append(cfg.NetInterfaceExcludeRegex, "Pseudo-Interface")
		cfg.CPULoadDataGather = []string{}
		cfg.CPUUtilTypes = []string{"user", "system", "idle"}
		cfg.VirtualMachinesStat = []string{"hyper-v"}
	} else {
		cfg.FSMetrics = append(cfg.FSMetrics, "inodes_used_percent")
	}

	return cfg
}

func NewMinimumConfig() *MinValuableConfig {
	cfg := defaultMinValuableConfig()

	cfg.applyEnv(false)

	if cfg.HubURL == "" {
		cfg.IOMode = IOModeFile
		if runtime.GOOS == "windows" {
			cfg.OutFile = "NUL"
		} else {
			cfg.OutFile = "/dev/null"
		}
	} else {
		cfg.IOMode = IOModeHTTP
	}

	return cfg
}

func defaultMinValuableConfig() *MinValuableConfig {
	return &MinValuableConfig{
		LogLevel: LogLevelError,
		IOMode:   IOModeHTTP,
	}
}

func secToDuration(secs float64) time.Duration {
	return time.Duration(int64(float64(time.Second) * secs))
}

func (mvc *MinValuableConfig) applyEnv(force bool) {
	if val, ok := os.LookupEnv("CAGENT_HUB_URL"); ok && ((mvc.HubURL == "") || force) {
		mvc.HubURL = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_USER"); ok && ((mvc.HubUser == "") || force) {
		mvc.HubUser = val
	}

	if val, ok := os.LookupEnv("CAGENT_HUB_PASSWORD"); ok && ((mvc.HubPassword == "") || force) {
		mvc.HubPassword = val
	}
}

func (cfg *Config) DumpToml() string {
	buff := &bytes.Buffer{}

	err := toml.NewEncoder(buff).Encode(cfg)
	if err != nil {
		log.Errorf("DumpToml error: %s", err.Error())
		return ""
	}

	return buff.String()
}

// TryUpdateConfigFromFile applies values from file in configFilePath to cfg if given file exists.
// it rewrites all cfg keys that present in the file
func TryUpdateConfigFromFile(cfg *Config, configFilePath string) error {
	_, err := os.Stat(configFilePath)
	if err != nil {
		return err
	}

	_, err = toml.DecodeFile(configFilePath, cfg)
	if err != nil {
		return err
	}

	// log.Printf("WARP: %+v", cfg)

	return nil
}

func SaveConfigFile(cfg interface{}, configFilePath string) error {
	var f *os.File
	var err error
	if f, err = os.OpenFile(configFilePath, os.O_WRONLY|os.O_CREATE, 0666); err != nil {
		return fmt.Errorf("failed to open the config file: '%s'", configFilePath)
	}

	defer func() {
		if err = f.Close(); err != nil {
			log.WithError(err).Errorf("failed to close config file: %s", configFilePath)
		}
	}()

	if _, err = f.Write(configAutogeneratedHeadline); err != nil {
		return fmt.Errorf("failed to write headline to config file")
	}

	err = toml.NewEncoder(f).Encode(cfg)
	if err != nil {
		return fmt.Errorf("failed to encode config to file")
	}

	return nil
}

func GenerateDefaultConfigFile(mvc *MinValuableConfig, configFilePath string) error {
	var err error

	if _, err = os.Stat(configFilePath); os.IsExist(err) {
		return fmt.Errorf("сonfig file already exists at path: %s", configFilePath)
	}

	configPathDir := filepath.Dir(configFilePath)
	if _, err := os.Stat(configPathDir); os.IsNotExist(err) {
		err := os.MkdirAll(configPathDir, os.ModePerm)
		if err != nil {
			return fmt.Errorf("failed to auto-create the default сonfig file directory '%s': %s", configPathDir, err.Error())
		}
	}

	var f *os.File
	if f, err = os.OpenFile(configFilePath, os.O_WRONLY|os.O_CREATE, 0666); err != nil {
		return fmt.Errorf("failed to create the default сonfig file at '%s': %s", configFilePath, err.Error())
	}

	defer func() {
		if err = f.Close(); err != nil {
			log.WithError(err).Errorf("failed to close сonfig file: %s", configFilePath)
		}
	}()

	if _, err = f.Write(configAutogeneratedHeadline); err != nil {
		return fmt.Errorf("failed to write headline to сonfig file")
	}

	err = toml.NewEncoder(f).Encode(mvc)
	if err != nil {
		return fmt.Errorf("failed to encode сonfig to file")
	}

	return err
}

func (cfg *Config) GetParsedNetInterfaceMaxSpeed() (uint64, error) {
	v := cfg.NetInterfaceMaxSpeed
	if v == "" {
		return 0, nil
	}
	if len(v) < 2 {
		return 0, fmt.Errorf("can't parse")
	}

	valueStr, unit := v[0:len(v)-1], v[len(v)-1]
	value, err := strconv.ParseFloat(valueStr, 0)
	if err != nil {
		return 0, err
	}
	if value <= 0.0 {
		return 0, fmt.Errorf("should be > 0.0")
	}

	switch unit {
	case 'K':
		return uint64(value * 1000), nil
	case 'M':
		return uint64(value * 1000 * 1000), nil
	case 'G':
		return uint64(value * 1000 * 1000 * 1000), nil
	}

	return 0, fmt.Errorf("unsupported unit: %c", unit)
}

func (cfg *Config) validate() error {
	if cfg.HubProxy != "" {
		if !strings.HasPrefix(cfg.HubProxy, "http") {
			cfg.HubProxy = "http://" + cfg.HubProxy
		}

		if _, err := url.Parse(cfg.HubProxy); err != nil {
			return fmt.Errorf("failed to parse 'hub_proxy' URL")
		}
	}

	if cfg.Interval < minIntervalValue {
		return fmt.Errorf("interval value must be >= %.1f", minIntervalValue)
	}

	if cfg.HeartbeatInterval < minHeartbeatIntervalValue {
		return fmt.Errorf("heartbeat value must be >= %.1f", minHeartbeatIntervalValue)
	}

	if !common.StrInSlice(cfg.OperationMode, operationModes) {
		return fmt.Errorf("invalid operation_mode supplied. Must be one of %v", operationModes)
	}

	_, err := cfg.GetParsedNetInterfaceMaxSpeed()
	if err != nil {
		return fmt.Errorf("invalid net_interface_max_speed value supplied: %s", err.Error())
	}

	if cfg.HubRequestTimeout < minHubRequestTimeout || cfg.HubRequestTimeout > maxHubRequestTimeout {
		return fmt.Errorf("hub_request_timeout must be between %d and %d", minHubRequestTimeout, maxHubRequestTimeout)
	}

	return nil
}

// HandleAllConfigSetup prepares Config for Cagent with parameters specified in file
// if Config file does not exist default one is created in form of MinValuableConfig
func HandleAllConfigSetup(configFilePath string) (*Config, error) {
	cfg := NewConfig()

	err := TryUpdateConfigFromFile(cfg, configFilePath)
	// If the Config file does not exist create a default Config at configFilePath
	if os.IsNotExist(err) {
		mvc := NewMinimumConfig()
		if err = GenerateDefaultConfigFile(mvc, configFilePath); err != nil {
			return nil, err
		}

		cfg.MinValuableConfig = *mvc
	} else if err != nil {
		if strings.Contains(err.Error(), "cannot load TOML value of type int64 into a Go float") {
			return nil, fmt.Errorf("Config load error: please use numbers with a decimal point for numerical values")
		}
		return nil, fmt.Errorf("Config load error: %s", err.Error())
	}

	if err = cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
