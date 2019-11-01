package raid

import (
	"regexp"
	"strconv"
	"strings"
)

type raidArrays []raidInfo

type raidInfo struct {
	Name         string
	Type         string
	State        string
	RaidLevel    int
	Devices      []string
	Inactive     []int
	Active       []int
	Failed       []int
	IsRebuilding bool
}

var raidStatusRegex = regexp.MustCompile(`\[([U_]+)\]`)

func (r raidInfo) GetFailedAndMissingPhysicalDevices() (failedDevices []string, missingDevicesCount int) {
	for _, deviceIndex := range r.Failed {
		if deviceIndex < len(r.Devices) {
			failedDevices = append(failedDevices, r.Devices[deviceIndex])
		}
	}

	for _, deviceIndex := range r.Inactive {
		if deviceIndex >= len(r.Devices) {
			missingDevicesCount++
		}
	}
	return failedDevices, missingDevicesCount
}

func parseMdstat(data string) raidArrays {
	var raids []raidInfo
	lines := strings.Split(data, "\n")

	for n, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "Personalities") || strings.HasPrefix(line, "unused") {
			continue
		}

		parts := strings.Split(line, " ")
		if len(parts) < 5 || parts[1] != ":" {
			continue
		}
		raidType := parts[3]
		level, err := strconv.Atoi(strings.TrimPrefix(raidType, "raid"))
		if err != nil {
			log.WithError(err).Warnf("could not determine raid level from line '%s'", line)
			level = -1
		}
		raid := raidInfo{Name: parts[0], State: parts[2], Type: raidType, RaidLevel: level, Devices: parts[4:]}

		raid.Devices = parts[4:]
		for i, device := range raid.Devices {
			p := strings.Index(device, "[")
			if p > 0 {
				raid.Devices[i] = device[0:p]
				if strings.Contains(device, "(F)") {
					raid.Failed = append(raid.Failed, i)
				}
			}
		}

		if len(lines) <= n+3 {
			log.Errorf("error parsing %s: too few lines for md device", raid.Name)
			return raids
		}

		raid.Inactive, raid.Active = parseStatusLine(lines[n+1])

		syncLineIdx := n + 2
		if strings.Contains(lines[n+2], "bitmap") { // skip bitmap line
			syncLineIdx++
		}

		isRecovering := strings.Contains(lines[syncLineIdx], "recovery")
		if isRecovering {
			raid.IsRebuilding = true
		}

		raids = append(raids, raid)
	}
	return raids
}

func parseStatusLine(line string) ([]int, []int) {
	var inactiveDevs, activeDevs []int
	matches := raidStatusRegex.FindStringSubmatch(line)
	if len(matches) > 0 {
		// Parse raid array status from mdstat output e.g. "[UUU_]"
		// if device is up("U") or down/missing ("_")
		for i := 0; i < len(matches[1]); i++ {
			if matches[1][i:i+1] == "_" {
				inactiveDevs = append(inactiveDevs, i)
			} else if matches[1][i:i+1] == "U" {
				activeDevs = append(activeDevs, i)
			}
		}
	}
	return inactiveDevs, activeDevs
}