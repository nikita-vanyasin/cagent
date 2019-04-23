// +build !windows

package hwinfo

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudradar-monitoring/dmidecode"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/cloudradar-monitoring/cagent/pkg/common"
)

type cpuInfo struct {
	manufacturer      string
	manufacturingInfo string
	description       string
	coreCount         string
	coreEnabled       string
	threadCount       string
}

var (
	Timeout    = 3 * time.Second
	ErrTimeout = errors.New("invoker: command timed out")
)

type Invoker interface {
	Command(string, ...string) ([]byte, error)
	CommandWithContext(context.Context, string, ...string) ([]byte, error)
}

type Invoke struct{}

func (i Invoke) Command(name string, arg ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	return i.CommandWithContext(ctx, name, arg...)
}

func (i Invoke) CommandWithContext(ctx context.Context, name string, arg ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, arg...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return buf.Bytes(), err
	}

	if err := cmd.Wait(); err != nil {
		return buf.Bytes(), err
	}

	return buf.Bytes(), nil
}

func RunCommandWithContext(ctx context.Context, name string, arg ...string) ([]byte, error) {
	var invoke Invoke

	return invoke.CommandWithContext(ctx, name, arg...)
}

func isCommandAvailable(name string) bool {
	cmd := exec.Command("/bin/sh", "-c", "command", "-v", name)
	if err := cmd.Run(); err != nil {
		return false
	}

	return true
}

func fetchInventory() (map[string]interface{}, error) {
	res := make(map[string]interface{})
	errorCollector := common.ErrorCollector{}

	pciDevices, err := listPCIDevices()
	errorCollector.Add(err)
	if len(pciDevices) > 0 {
		res["pci.list"] = pciDevices
	}

	usbDevices, err := listUSBDevices()
	errorCollector.Add(err)
	if len(usbDevices) > 0 {
		res["usb.list"] = usbDevices
	}

	displays, err := listDisplays()
	errorCollector.Add(err)
	if len(displays) > 0 {
		res["displays.list"] = displays
	}

	cpus, err := listCPUs()
	errorCollector.Add(err)
	if len(cpus) > 0 {
		encodedCpus := make(map[string]interface{})

		for i := range cpus {
			encodedCpus[fmt.Sprintf("cpu.%d.manufacturer", i)] = cpus[i].manufacturer
			encodedCpus[fmt.Sprintf("cpu.%d.manufacturing_info", i)] = cpus[i].manufacturingInfo
			encodedCpus[fmt.Sprintf("cpu.%d.description", i)] = cpus[i].description
			encodedCpus[fmt.Sprintf("cpu.%d.core_count", i)] = cpus[i].coreCount
			encodedCpus[fmt.Sprintf("cpu.%d.core_enabled", i)] = cpus[i].coreEnabled
			encodedCpus[fmt.Sprintf("cpu.%d.thread_count", i)] = cpus[i].threadCount
		}

		res = common.MergeStringMaps(res, encodedCpus)
	}

	dmiDecodeResults, err := retrieveInfoUsingDmiDecode()
	errorCollector.Add(err)
	if len(dmiDecodeResults) > 0 {
		res = common.MergeStringMaps(res, dmiDecodeResults)
	}

	return res, errorCollector.Combine()
}

func retrieveInfoUsingDmiDecode() (map[string]interface{}, error) {
	if !isCommandAvailable("dmidecode") {
		log.Infof("[HWINFO] dmidecode is not present. Skipping retrieval of baseboard, CPU and RAM info...")
		return nil, nil
	}

	cmd := exec.Command("/bin/sh", "-c", dmidecodeCommand())

	stdoutBuffer := bytes.Buffer{}
	cmd.Stdout = bufio.NewWriter(&stdoutBuffer)

	stderrBuffer := bytes.Buffer{}
	cmd.Stderr = bufio.NewWriter(&stderrBuffer)

	if err := cmd.Run(); err != nil {
		stderrBytes, _ := ioutil.ReadAll(bufio.NewReader(&stderrBuffer))
		stderr := string(stderrBytes)
		if strings.Contains(stderr, "/dev/mem: Operation not permitted") {
			log.Infof("[HWINFO] there was an error while executing '%s': %s\nProbably 'CONFIG_STRICT_DEVMEM' kernel configuration option is enabled. Please refer to kernel configuration manual.", dmidecodeCommand(), stderr)
			return nil, nil
		}
		return nil, errors.Wrap(err, "execute dmidecode")
	}

	dmi, err := dmidecode.Unmarshal(bufio.NewReader(&stdoutBuffer))
	if err != nil {
		return nil, errors.Wrap(err, "unmarshal dmi")
	}

	res := make(map[string]interface{})

	// all below requests are based on parsed data returned by dmidecode.Unmarshal
	// refer to doc dmidecode.Get to get description of function behavior
	var reqSys []dmidecode.ReqBaseBoard
	if err = dmi.Get(&reqSys); err == nil {
		res["baseboard.manufacturer"] = reqSys[0].Manufacturer
		res["baseboard.model"] = reqSys[0].Version
		res["baseboard.serial_number"] = reqSys[0].SerialNumber
	} else if err != dmidecode.ErrNotFound {
		log.WithError(err).Info("[HWINFO] failed fetching baseboard info")
	}

	var reqMem []dmidecode.ReqPhysicalMemoryArray
	if err = dmi.Get(&reqMem); err == nil {
		res["ram.number_of_modules"] = reqMem[0].NumberOfDevices
	} else if err != dmidecode.ErrNotFound {
		log.WithError(err).Info("[HWINFO] failed fetching memory array info")
	}

	var reqMemDevs []dmidecode.ReqMemoryDevice
	if err = dmi.Get(&reqMemDevs); err == nil {
		for i := range reqMemDevs {
			if reqMemDevs[i].Size == -1 {
				continue
			}
			res[fmt.Sprintf("ram.%d.size_B", i)] = reqMemDevs[i].Size
			res[fmt.Sprintf("ram.%d.type", i)] = reqMemDevs[i].Type
		}
	} else if err != dmidecode.ErrNotFound {
		log.WithError(err).Info("[HWINFO] failed fetching memory device info")
	}

	return res, nil
}
